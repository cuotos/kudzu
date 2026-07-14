package config

import (
	"os"
	"path/filepath"
	"testing"
)

// allEnv lists every variable Load reads, so a test can neutralise the host
// environment before asserting on defaults.
var allEnv = []string{
	"KUDZU_ADDR", "REDIS_ADDR", "REDIS_PASSWORD", "REDIS_DB",
	"REQUIRED_CHECK_CONTEXT", "GITHUB_API_BASE_URL", "KUDZU_REQUIRE_READ_AUTH",
	"BREAKER_FAILURE_THRESHOLD", "KUDZU_WRITE_TOKENS",
	"GITHUB_APP_ID", "GITHUB_APP_INSTALLATION_ID",
	"GITHUB_APP_PRIVATE_KEY", "GITHUB_APP_PRIVATE_KEY_FILE",
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range allEnv {
		t.Setenv(k, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Addr != ":8080" {
		t.Errorf("Addr = %q, want :8080", c.Addr)
	}
	if c.RedisAddr != "localhost:6379" {
		t.Errorf("RedisAddr = %q, want localhost:6379", c.RedisAddr)
	}
	if c.RedisDB != 0 {
		t.Errorf("RedisDB = %d, want 0", c.RedisDB)
	}
	if c.CheckContext != "kudzu-gate" {
		t.Errorf("CheckContext = %q, want kudzu-gate", c.CheckContext)
	}
	if c.GitHubAPIBaseURL != "https://api.github.com/" {
		t.Errorf("GitHubAPIBaseURL = %q", c.GitHubAPIBaseURL)
	}
	if c.RequireReadAuth {
		t.Error("RequireReadAuth = true, want false")
	}
	if c.FailureThreshold != 1 {
		t.Errorf("FailureThreshold = %d, want 1", c.FailureThreshold)
	}
	if len(c.WriteTokens) != 0 {
		t.Errorf("WriteTokens = %v, want empty", c.WriteTokens)
	}
	if c.EvictionEnabled() {
		t.Error("EvictionEnabled = true with no GitHub App creds")
	}
}

func TestLoadFromEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("KUDZU_ADDR", ":9090")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("REDIS_PASSWORD", "s3cret")
	t.Setenv("REDIS_DB", "3")
	t.Setenv("REQUIRED_CHECK_CONTEXT", "deploy-gate")
	t.Setenv("KUDZU_REQUIRE_READ_AUTH", "true")
	t.Setenv("BREAKER_FAILURE_THRESHOLD", "5")
	t.Setenv("KUDZU_WRITE_TOKENS", "a, b ,, c")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Addr != ":9090" || c.RedisAddr != "redis:6379" || c.RedisPassword != "s3cret" {
		t.Errorf("addr/redis not parsed: %+v", c)
	}
	if c.RedisDB != 3 {
		t.Errorf("RedisDB = %d, want 3", c.RedisDB)
	}
	if c.CheckContext != "deploy-gate" {
		t.Errorf("CheckContext = %q", c.CheckContext)
	}
	if !c.RequireReadAuth {
		t.Error("RequireReadAuth = false, want true")
	}
	if c.FailureThreshold != 5 {
		t.Errorf("FailureThreshold = %d, want 5", c.FailureThreshold)
	}
	if got, want := c.WriteTokens, []string{"a", "b", "c"}; !equalStrings(got, want) {
		t.Errorf("WriteTokens = %v, want %v (blanks trimmed)", got, want)
	}
}

func TestLoadInvalidIntsFallBack(t *testing.T) {
	// REDIS_DB and BREAKER_FAILURE_THRESHOLD silently fall back to defaults on
	// a non-numeric value (getint), rather than failing the whole load.
	clearEnv(t)
	t.Setenv("REDIS_DB", "not-a-number")
	t.Setenv("BREAKER_FAILURE_THRESHOLD", "nope")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.RedisDB != 0 {
		t.Errorf("RedisDB = %d, want default 0", c.RedisDB)
	}
	if c.FailureThreshold != 1 {
		t.Errorf("FailureThreshold = %d, want default 1", c.FailureThreshold)
	}
}

func TestLoadInvalidAppIDErrors(t *testing.T) {
	clearEnv(t)
	t.Setenv("GITHUB_APP_ID", "abc")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for non-numeric GITHUB_APP_ID")
	}
}

func TestPrivateKeyInlineWins(t *testing.T) {
	clearEnv(t)
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "INLINE-PEM")
	t.Setenv("GITHUB_APP_PRIVATE_KEY_FILE", filepath.Join(t.TempDir(), "missing.pem"))
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(c.GitHubPrivateKey) != "INLINE-PEM" {
		t.Errorf("private key = %q, want inline value (file ignored when inline set)", c.GitHubPrivateKey)
	}
}

func TestPrivateKeyFromFile(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(path, []byte("FILE-PEM"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_APP_PRIVATE_KEY_FILE", path)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(c.GitHubPrivateKey) != "FILE-PEM" {
		t.Errorf("private key = %q, want file contents", c.GitHubPrivateKey)
	}
}

func TestPrivateKeyFileMissingErrors(t *testing.T) {
	clearEnv(t)
	t.Setenv("GITHUB_APP_PRIVATE_KEY_FILE", filepath.Join(t.TempDir(), "nope.pem"))
	if _, err := Load(); err == nil {
		t.Fatal("expected error reading missing private key file")
	}
}

func TestEvictionEnabled(t *testing.T) {
	cases := []struct {
		name string
		c    Config
		want bool
	}{
		{"all present", Config{GitHubAppID: 1, GitHubInstallationID: 2, GitHubPrivateKey: []byte("k")}, true},
		{"no app id", Config{GitHubInstallationID: 2, GitHubPrivateKey: []byte("k")}, false},
		{"no installation", Config{GitHubAppID: 1, GitHubPrivateKey: []byte("k")}, false},
		{"no key", Config{GitHubAppID: 1, GitHubInstallationID: 2}, false},
	}
	for _, c := range cases {
		if got := c.c.EvictionEnabled(); got != c.want {
			t.Errorf("%s: EvictionEnabled = %v, want %v", c.name, got, c.want)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
