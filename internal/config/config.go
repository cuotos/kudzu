// Package config loads Kudzu's configuration from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config is the full service configuration.
type Config struct {
	Addr            string // listen address, e.g. ":8080"
	RedisAddr       string // host:port
	RedisPassword   string
	RedisDB         int
	WriteTokens     []string // bearer tokens accepted on write endpoints
	RequireReadAuth bool     // also require a token on read endpoints

	FailureThreshold int    // consecutive failures that trip the breaker
	CheckContext     string // commit-status context used for eviction

	// GitHub App credentials for proactive eviction. If AppID or PrivateKey is
	// empty, eviction is disabled (a no-op evicter is used).
	GitHubAppID          int64
	GitHubInstallationID int64
	GitHubPrivateKey     []byte // PEM bytes
	GitHubAPIBaseURL     string // e.g. https://github.acme.com/api/v3/ for GHES
}

// Load reads configuration from environment variables, applying defaults.
func Load() (Config, error) {
	c := Config{
		Addr:             getenv("KUDZU_ADDR", ":8080"),
		RedisAddr:        getenv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:    os.Getenv("REDIS_PASSWORD"),
		CheckContext:     getenv("REQUIRED_CHECK_CONTEXT", "kudzu-gate"),
		GitHubAPIBaseURL: getenv("GITHUB_API_BASE_URL", "https://api.github.com/"),
		RequireReadAuth:  getbool("KUDZU_REQUIRE_READ_AUTH", false),
		FailureThreshold: getint("BREAKER_FAILURE_THRESHOLD", 1),
		RedisDB:          getint("REDIS_DB", 0),
	}

	c.WriteTokens = splitNonEmpty(os.Getenv("KUDZU_WRITE_TOKENS"))

	var err error
	if v := os.Getenv("GITHUB_APP_ID"); v != "" {
		if c.GitHubAppID, err = strconv.ParseInt(v, 10, 64); err != nil {
			return c, fmt.Errorf("GITHUB_APP_ID: %w", err)
		}
	}
	if v := os.Getenv("GITHUB_APP_INSTALLATION_ID"); v != "" {
		if c.GitHubInstallationID, err = strconv.ParseInt(v, 10, 64); err != nil {
			return c, fmt.Errorf("GITHUB_APP_INSTALLATION_ID: %w", err)
		}
	}
	// The private key may be supplied inline or via a mounted file path.
	if v := os.Getenv("GITHUB_APP_PRIVATE_KEY"); v != "" {
		c.GitHubPrivateKey = []byte(v)
	} else if p := os.Getenv("GITHUB_APP_PRIVATE_KEY_FILE"); p != "" {
		if c.GitHubPrivateKey, err = os.ReadFile(p); err != nil {
			return c, fmt.Errorf("GITHUB_APP_PRIVATE_KEY_FILE: %w", err)
		}
	}
	return c, nil
}

// EvictionEnabled reports whether GitHub App credentials are present.
func (c Config) EvictionEnabled() bool {
	return c.GitHubAppID != 0 && c.GitHubInstallationID != 0 && len(c.GitHubPrivateKey) > 0
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func getint(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getbool(k string, def bool) bool {
	if v := os.Getenv(k); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func splitNonEmpty(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
