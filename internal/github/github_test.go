package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// testKeyPEM generates a throwaway RSA private key so ghinstallation can sign
// the app JWT.
func testKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}

func TestEvictPostsFailureStatuses(t *testing.T) {
	var (
		mu       sync.Mutex
		statuses []string // "sha:state:context"
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			// GitHub App installation token mint.
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "ghs_testtoken",
				"expires_at": "2099-01-01T00:00:00Z",
			})
		case strings.Contains(r.URL.Path, "/git/matching-refs/heads/gh-readonly-queue/main/"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"ref": "refs/heads/gh-readonly-queue/main/pr-1", "object": map[string]string{"sha": "sha1"}},
				{"ref": "refs/heads/gh-readonly-queue/main/pr-2", "object": map[string]string{"sha": "sha2"}},
			})
		case strings.Contains(r.URL.Path, "/statuses/"):
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			sha := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			mu.Lock()
			statuses = append(statuses, sha+":"+body["state"]+":"+body["context"])
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := New(123, 456, testKeyPEM(t), srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}

	shas, err := c.Evict(context.Background(), "octo/orders", "main", "kudzu-gate", "tripped")
	if err != nil {
		t.Fatal(err)
	}
	if len(shas) != 2 {
		t.Fatalf("want 2 evicted SHAs, got %v", shas)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(statuses) != 2 {
		t.Fatalf("want 2 statuses posted, got %v", statuses)
	}
	for _, s := range statuses {
		if !strings.HasSuffix(s, ":failure:kudzu-gate") {
			t.Errorf("unexpected status %q", s)
		}
	}
}

func TestEvictInvalidRepo(t *testing.T) {
	c := &Client{http: http.DefaultClient, apiBase: "https://example.com/"}
	if _, err := c.Evict(context.Background(), "no-slash", "main", "ctx", "d"); err == nil {
		t.Fatal("expected error for invalid repo")
	}
}
