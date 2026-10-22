package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

// Exercise the packaged/installed executable through actual HTTP and cookie rules.
func TestTOTPBinaryJourney(t *testing.T) {
	binary := os.Getenv("SHELL_TEST_GATEWAY_BINARY")
	if binary == "" {
		t.Skip("set SHELL_TEST_GATEWAY_BINARY for installed-binary acceptance")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	_, port, _ := net.SplitHostPort(address)
	origin := "http://localhost:" + port
	var environment []string
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "SHELL_") && !strings.HasPrefix(v, "AUTH_") {
			environment = append(environment, v)
		}
	}
	environment = append(environment, "SHELL_AUTH_MODE=local", "SHELL_LISTEN="+address, "SHELL_PUBLIC_URL="+origin, "SHELL_DATA_DIR="+t.TempDir(), "SHELL_ALLOWED_EMAILS=user@example.com", "SHELL_NETMON_INTERVAL=0", "SHELL_TTYD_URL=http://127.0.0.1:1")
	command := exec.Command(binary, "serve")
	command.Env = environment
	// Retain diagnostics in an isolated file without concurrent buffer reads.
	logFile, err := os.CreateTemp(t.TempDir(), "gateway-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logFile.Close() }()
	command.Stdout = logFile
	command.Stderr = logFile
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	ready := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		r, err := client.Get(origin + "/healthz")
		if err == nil {
			_ = r.Body.Close()
			if r.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		logs, _ := os.ReadFile(logFile.Name())
		t.Fatalf("binary did not start: %s", logs)
	}
	invite := exec.Command(binary, "invite", "--email", "user@example.com", "--json")
	invite.Env = environment
	raw, err := invite.Output()
	if err != nil {
		t.Fatal("invite command", err)
	}
	var result struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	request := func(path string, body any, want int) map[string]any {
		t.Helper()
		var reader io.Reader
		method := "GET"
		if body != nil {
			b, _ := json.Marshal(body)
			reader = bytes.NewReader(b)
			method = "POST"
		}
		req, _ := http.NewRequest(method, origin+path, reader)
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != want {
			t.Fatalf("%s: status %d, expected %d", path, res.StatusCode, want)
		}
		var value map[string]any
		if err = json.NewDecoder(res.Body).Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	request("/api/password/register", map[string]string{"email": "user@example.com", "password": "a long binary test password", "token": result.Data.Token}, 200)
	setup := request("/api/totp/setup", map[string]string{}, 200)
	code, _ := totp.GenerateCode(setup["secret"].(string), time.Now())
	enabled := request("/api/totp/enable", map[string]string{"setupId": setup["setupId"].(string), "code": code}, 200)
	recovery := enabled["recoveryCodes"].([]any)
	request("/logout", map[string]string{}, 200)
	login := request("/api/password/login", map[string]string{"email": "user@example.com", "password": "a long binary test password"}, 200)
	if login["mfaRequired"] != true {
		t.Fatal("binary login bypassed 2FA")
	}
	request("/api/totp", nil, 401)
	request("/api/totp/verify", map[string]string{"code": recovery[0].(string)}, 200)
	if status := request("/api/totp", nil, 200); status["enabled"] != true || status["recoveryCodesRemaining"] != float64(9) {
		t.Fatal("binary factor readback", status)
	}
	reset := exec.Command(binary, "totp-reset", "--email", "user@example.com", "--json")
	reset.Env = environment
	raw, err = reset.Output()
	if err != nil {
		t.Fatal("operator reset", err)
	}
	if !bytes.Contains(raw, []byte(`"state":"reset"`)) {
		t.Fatal("reset returned incorrect state")
	}
	request("/api/totp", nil, 401)
	request("/api/password/login", map[string]string{"email": "user@example.com", "password": "a long binary test password"}, 200)
	if request("/api/totp", nil, 200)["enabled"] != false {
		t.Fatal("operator reset not active")
	}
	t.Log("installed binary: enrollment, pending-login isolation, recovery consumption, operator reset and session invalidation passed")
}
