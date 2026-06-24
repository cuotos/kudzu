package httpapi

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// tokenAuth holds the set of accepted bearer tokens.
type tokenAuth struct {
	tokens [][]byte
}

func newTokenAuth(tokens []string) tokenAuth {
	t := tokenAuth{}
	for _, tok := range tokens {
		if tok != "" {
			t.tokens = append(t.tokens, []byte(tok))
		}
	}
	return t
}

// valid reports whether the request carries an accepted bearer token. With no
// configured tokens, all requests are rejected on protected routes (fail closed).
func (a tokenAuth) valid(r *http.Request) bool {
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer"))
	if got == "" || len(a.tokens) == 0 {
		return false
	}
	gb := []byte(got)
	for _, want := range a.tokens {
		if subtle.ConstantTimeCompare(gb, want) == 1 {
			return true
		}
	}
	return false
}

// require wraps a handler so it only runs for authenticated requests.
func (a tokenAuth) require(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.valid(r) {
			writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
			return
		}
		next(w, r)
	}
}
