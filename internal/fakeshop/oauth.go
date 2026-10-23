package fakeshop

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// authorize is the consent screen, collapsed: it approves at once and sends
// the merchant back to redirect_uri with a code, the account, the state and
// the scopes, signed hmac-sha256/hex over the sorted query with the client
// secret (manifest auth.callback_verify; no timestamp).
func (s *Shop) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if q.Get("client_id") != s.opt.ClientID || err != nil || redirect.Scheme != "https" || q.Get("state") == "" {
		writeJSON(w, 400, map[string]string{"error": "invalid_request"})
		return
	}
	s.mu.Lock()
	code := fmt.Sprintf("code-%d", len(s.codes)+1)
	s.codes[code] = codeGrant{challenge: q.Get("code_challenge")}
	s.mu.Unlock()
	cb := url.Values{"code": {code}, "shop": {s.opt.Account}, "state": {q.Get("state")}, "scope": {q.Get("scope")}}
	cb.Set("hmac", s.SignQuery(cb))
	redirect.RawQuery = cb.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

// SignQuery signs a callback query (hex HMAC-SHA256 of the canonical string).
func (s *Shop) SignQuery(q url.Values) string {
	m := hmac.New(sha256.New, []byte(s.opt.Secret))
	m.Write([]byte(CanonicalSortedQuery(q, "hmac")))
	return hex.EncodeToString(m.Sum(nil))
}

// token exchanges a code (with the PKCE verifier) or a refresh token.
// Refresh tokens rotate: the old one is dead the moment a new one is issued.
func (s *Shop) token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.ParseForm() != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid_request"})
		return
	}
	f := r.PostForm
	if f.Get("client_id") != s.opt.ClientID || f.Get("client_secret") != s.opt.Secret {
		writeJSON(w, 401, map[string]string{"error": "invalid_client"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch f.Get("grant_type") {
	case "authorization_code":
		g, ok := s.codes[f.Get("code")]
		if !ok {
			writeJSON(w, 400, map[string]string{"error": "invalid_grant"})
			return
		}
		delete(s.codes, f.Get("code")) // a code is single-use
		if g.challenge != "" {
			sum := sha256.Sum256([]byte(f.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
				writeJSON(w, 400, map[string]string{"error": "invalid_grant"})
				return
			}
		}
	case "refresh_token":
		if s.refresh == "" || f.Get("refresh_token") != s.refresh {
			writeJSON(w, 400, map[string]string{"error": "invalid_grant"})
			return
		}
	default:
		writeJSON(w, 400, map[string]string{"error": "unsupported_grant_type"})
		return
	}
	s.refreshN++
	s.refresh = fmt.Sprintf("demo-refresh-%d", s.refreshN)
	writeJSON(w, 200, map[string]any{
		"access_token": s.opt.Token, "token_type": "bearer", "expires_in": 3600,
		"refresh_token": s.refresh, "scope": strings.Join([]string{"products", "inventory", "orders"}, ","),
	})
}
