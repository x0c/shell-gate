package authkit

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ConfigFromEnv reads the optional external-provider policy. No setting disables authentication.
func ConfigFromEnv() (Config, error) {
	cfg := Config{Secret: []byte(os.Getenv("AUTH_JWT_SECRET")), Audience: os.Getenv("AUTH_JWT_AUDIENCE")}
	if cfg.Audience == "" {
		cfg.Audience = "authenticated"
	}
	for _, v := range strings.Split(os.Getenv("AUTH_JWT_ISSUERS"), ",") {
		if v = strings.TrimSpace(v); v != "" {
			cfg.Issuers = append(cfg.Issuers, v)
		}
	}
	if raw := os.Getenv("AUTH_JWT_LEEWAY_SECONDS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("AUTH_JWT_LEEWAY_SECONDS must be an integer")
		}
		cfg.LeewaySeconds = n
	}
	if raw := os.Getenv("AUTH_DEV_BYPASS"); raw != "" && raw != "false" && raw != "0" {
		return Config{}, fmt.Errorf("authentication bypass is not supported")
	}
	return cfg, nil
}
