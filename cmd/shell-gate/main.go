// Command shell-gate runs the gateway and local operator tools.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/x0c/shell-gate/internal/server"
	"github.com/x0c/shell-gate/internal/server/store"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		jsonOutput := false
		for _, arg := range os.Args[1:] {
			jsonOutput = jsonOutput || arg == "--json" || arg == "--json=true"
		}
		if jsonOutput {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": false, "error": map[string]string{"code": "request_failed", "message": err.Error()}})
		} else {
			log.Printf("%v", err)
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	command := "serve"
	if len(args) > 0 {
		command = args[0]
		args = args[1:]
	}
	switch command {
	case "version", "--version":
		if len(args) == 1 && args[0] == "--json" {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": true, "data": map[string]string{"version": version}})
		}
		if len(args) != 0 {
			return fmt.Errorf("unexpected version arguments")
		}
		fmt.Println(version)
		return nil
	case "help", "--help", "-h":
		fmt.Println("Usage: shell-gate [serve|version|doctor|invite|totp-reset]\n\nConfiguration: SHELL_PUBLIC_URL, SHELL_ALLOWED_EMAILS, SHELL_TTYD_URL.\nOptional storage: SHELL_DATA_DIR, SHELL_DATABASE_URL.\nInvite: shell-gate invite --email user@example.com [--ttl 1h] [--json]\nLost authenticator: shell-gate totp-reset --email user@example.com [--dry-run] [--json]")
		return nil
	case "serve", "doctor", "invite", "totp-reset":
	default:
		return fmt.Errorf("unknown command %q; run shell-gate help", command)
	}
	cfg, err := server.ConfigFromEnv()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	if command == "doctor" {
		flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
		jsonOutput := flags.Bool("json", false, "structured output")
		if err := flags.Parse(args); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("unexpected doctor arguments")
		}
		result := map[string]any{"version": version, "auth": cfg.AuthMode, "machines": len(cfg.Machines), "configuration": "valid"}
		if *jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": true, "data": result})
		}
		fmt.Printf("ShellGate %s: configuration valid; %s authentication; %d terminal(s)\n", version, cfg.AuthMode, len(cfg.Machines))
		return nil
	}
	if command == "totp-reset" {
		flags := flag.NewFlagSet("totp-reset", flag.ContinueOnError)
		email := flags.String("email", "", "allowed account email")
		jsonOutput := flags.Bool("json", false, "structured output")
		dryRun := flags.Bool("dry-run", false, "validate without writing")
		if err := flags.Parse(args); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("unexpected totp-reset arguments")
		}
		*email = strings.ToLower(strings.TrimSpace(*email))
		if _, ok := cfg.AllowedEmails[*email]; !ok {
			return fmt.Errorf("email must be in SHELL_ALLOWED_EMAILS")
		}
		result := map[string]string{"email": *email, "state": "planned"}
		if !*dryRun {
			st, err := store.Open(cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			if err := st.ResetTOTP(*email); err != nil {
				return fmt.Errorf("reset two-factor authentication: %w", err)
			}
			result["state"] = "reset"
		}
		if *jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": true, "data": result})
		}
		fmt.Printf("Two-factor reset for %s: %s.\n", *email, result["state"])
		return nil
	}
	if command == "invite" {
		flags := flag.NewFlagSet("invite", flag.ContinueOnError)
		email := flags.String("email", "", "allowed account email")
		ttl := flags.Duration("ttl", time.Hour, "invitation lifetime (up to 24h)")
		jsonOutput := flags.Bool("json", false, "structured output")
		dryRun := flags.Bool("dry-run", false, "validate without writing")
		if err := flags.Parse(args); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("unexpected invite arguments")
		}
		if cfg.AuthMode != "local" {
			return fmt.Errorf("invitations require local authentication")
		}
		*email = strings.ToLower(strings.TrimSpace(*email))
		if _, ok := cfg.AllowedEmails[*email]; !ok {
			return fmt.Errorf("email must be in SHELL_ALLOWED_EMAILS")
		}
		if *ttl <= 0 || *ttl > 24*time.Hour {
			return fmt.Errorf("invitation lifetime must be positive and at most 24h")
		}
		result := map[string]any{"email": *email, "expiresIn": ttl.String(), "state": "planned"}
		if !*dryRun {
			st, err := store.Open(cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			token, err := st.Invite(*email, *ttl)
			if err != nil {
				return err
			}
			result["token"] = token
			result["state"] = "created"
		}
		if *jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": true, "data": result})
		}
		if *dryRun {
			fmt.Println("Invitation configuration is valid; no changes made.")
			return nil
		}
		fmt.Printf("Open %s/login and select Set up or recover an account.\nEmail: %s\nInvitation code: %s\nExpires in %s. Share privately; this code can be used once.\n", cfg.PublicURL, *email, result["token"], *ttl)
		return nil
	}
	if len(args) > 0 {
		return fmt.Errorf("serve does not accept arguments; configure with SHELL_* settings")
	}
	srv, err := server.New(cfg)
	if err != nil {
		return fmt.Errorf("create server: %w", err)
	}
	defer func() {
		if err := srv.Shutdown(); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() { log.Printf("ShellGate %s listening on %s", version, cfg.Listen); errCh <- srv.ListenAndServe() }()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		return nil
	}
}
