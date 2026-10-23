package fakeshop

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The fake shop is itself test infrastructure; these tests pin the behaviour
// the adapter tests and the conformance suite lean on.

func do(s *Shop, method, target, body string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func authed(s *Shop) map[string]string { return map[string]string{Header: s.Token()} }

func TestAPIRequiresTheToken(t *testing.T) {
	s := New(Options{})
	if c := do(s, "GET", "/api/shop", "", nil).Code; c != 401 {
		t.Fatalf("no token: %d", c)
	}
	if c := do(s, "GET", "/api/shop", "", map[string]string{Header: "wrong"}).Code; c != 401 {
		t.Fatalf("wrong token: %d", c)
	}
	if c := do(s, "GET", "/api/shop", "", authed(s)).Code; c != 200 {
		t.Fatalf("token: %d", c)
	}
}

func TestThereIsNoDeleteAnywhere(t *testing.T) {
	s := New(Options{})
	for _, p := range []string{"/api/products/p-0001", "/api/orders/o-9001", "/api/inventory/v-0001-1", "/api/shop"} {
		if c := do(s, "DELETE", p, "", authed(s)).Code; c != 405 {
			t.Errorf("DELETE %s = %d", p, c)
		}
	}
	if s.Writes() != 4 {
		t.Fatalf("writes %d: a DELETE attempt must count as a write", s.Writes())
	}
	if len(s.ProductList()) != 50 || len(s.Orders()) != 3 {
		t.Fatal("something was deleted")
	}
}

func TestProductsPageThroughAllFiftyWithVariants(t *testing.T) {
	s := New(Options{})
	token, n, variants := "", 0, 0
	for {
		w := do(s, "GET", "/api/products?limit=20&page_token="+token, "", authed(s))
		var page struct {
			Products []Product `json:"products"`
			Next     string    `json:"next_page_token"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		n += len(page.Products)
		for _, p := range page.Products {
			variants += len(p.Variants)
		}
		if page.Next == "" {
			break
		}
		token = page.Next
	}
	if n != 50 || variants <= 50 {
		t.Fatalf("%d products, %d variants", n, variants)
	}
}

func TestThrottlingAnswers429ThenRecovers(t *testing.T) {
	s := New(Options{ThrottleEvery: 3})
	var codes []int
	for i := 0; i < 6; i++ {
		codes = append(codes, do(s, "GET", "/api/shop", "", authed(s)).Code)
	}
	want := []int{200, 200, 429, 200, 200, 429}
	for i := range want {
		if codes[i] != want[i] {
			t.Fatalf("codes %v, want %v", codes, want)
		}
	}
}

func TestInventoryIsAbsoluteWithCompareAndSet(t *testing.T) {
	s := New(Options{})
	v := "v-0001-1"
	start := s.Stock(v)
	put := func(body string) *httptest.ResponseRecorder {
		return do(s, "PUT", "/api/inventory/"+v, body, authed(s))
	}
	if c := put(`{"available":4,"expect":` + itoa(start) + `}`).Code; c != 200 || s.Stock(v) != 4 {
		t.Fatalf("set: %d %d", c, s.Stock(v))
	}
	w := put(`{"available":9,"expect":` + itoa(start) + `}`) // stale
	if w.Code != 409 || s.Stock(v) != 4 || !strings.Contains(w.Body.String(), `"available":4`) {
		t.Fatalf("stale: %d %s stock %d", w.Code, w.Body, s.Stock(v))
	}
	if c := put(`{"available":-1}`).Code; c != 422 {
		t.Fatalf("negative: %d", c)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestFulfilmentAndCancelAreIdempotentOnTheKey(t *testing.T) {
	s := New(Options{})
	post := func(path, key string) int {
		h := authed(s)
		if key != "" {
			h["Idempotency-Key"] = key
		}
		return do(s, "POST", path, `{"carrier":"x"}`, h).Code
	}
	if c := post("/api/orders/o-9001/fulfilments", ""); c != 400 {
		t.Fatalf("no key: %d", c)
	}
	if post("/api/orders/o-9001/fulfilments", "k1") != 200 || post("/api/orders/o-9001/fulfilments", "k1") != 200 {
		t.Fatal("a replay of the same key must answer the same")
	}
	if s.Performed("fulfil", "o-9001") != 1 {
		t.Fatalf("performed %d", s.Performed("fulfil", "o-9001"))
	}
	if c := post("/api/orders/o-9001/fulfilments", "k2"); c != 409 {
		t.Fatalf("a second fulfilment with a new key: %d", c)
	}
	if post("/api/orders/o-9002/cancel", "k3") != 200 || post("/api/orders/o-9002/cancel", "k4") != 409 {
		t.Fatal("cancel twice")
	}
}

func TestWebhookSignatureIsBase64HMACOverTheBody(t *testing.T) {
	s := New(Options{Secret: "invented-secret"})
	body := s.WebhookBody("evt-1", "orders/create", "o-9001")
	sig := s.SignWebhook(body)
	raw, err := base64.StdEncoding.DecodeString(sig)
	if err != nil || len(raw) != 32 {
		t.Fatalf("signature %q", sig)
	}
	if s.SignWebhook(append(body, ' ')) == sig {
		t.Fatal("the signature does not cover the body")
	}
}

func TestOAuthCodeGrantWithPKCEAndRotatingRefresh(t *testing.T) {
	s := New(Options{})
	verifier := "invented-code-verifier-0123456789-0123456789-0123"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	redirect := "https://cloud.example.test/ui/app/channels/oauth/callback"
	q := url.Values{"client_id": {s.opt.ClientID}, "redirect_uri": {redirect}, "state": {"st-1"}, "scope": {"products,inventory,orders"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	w := do(s, "GET", "/oauth/authorize?"+q.Encode(), "", nil)
	if w.Code != 302 {
		t.Fatalf("authorize: %d %s", w.Code, w.Body)
	}
	loc, _ := url.Parse(w.Header().Get("Location"))
	cb := loc.Query()
	if loc.Host != "cloud.example.test" || cb.Get("shop") != s.Account() || cb.Get("state") != "st-1" {
		t.Fatalf("callback %s", loc)
	}
	// The callback carries a signature the host can verify with the client secret.
	if cb.Get("hmac") != s.SignQuery(cb) || len(cb.Get("hmac")) != 64 {
		t.Fatal("callback not signed")
	}
	if _, err := hex.DecodeString(cb.Get("hmac")); err != nil {
		t.Fatal("hmac is not hex")
	}
	cb.Set("shop", "other.demo-shop.example")
	if cb.Get("hmac") == s.SignQuery(cb) {
		t.Fatal("the signature does not cover the account")
	}

	token := func(form url.Values) (int, map[string]any) {
		w := do(s, "POST", "/oauth/token", form.Encode(), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	base := url.Values{"client_id": {s.opt.ClientID}, "client_secret": {s.opt.Secret}}
	with := func(kv ...string) url.Values {
		v := url.Values{}
		for k, vs := range base {
			v[k] = vs
		}
		for i := 0; i < len(kv); i += 2 {
			v.Set(kv[i], kv[i+1])
		}
		return v
	}
	if c, _ := token(with("grant_type", "authorization_code", "code", loc.Query().Get("code"), "code_verifier", "wrong")); c != 400 {
		t.Fatalf("wrong verifier: %d", c)
	}
	// The code was spent by the failed attempt: single use.
	if c, _ := token(with("grant_type", "authorization_code", "code", loc.Query().Get("code"), "code_verifier", verifier)); c != 400 {
		t.Fatalf("reused code: %d", c)
	}
	w = do(s, "GET", "/oauth/authorize?"+q.Encode(), "", nil)
	loc, _ = url.Parse(w.Header().Get("Location"))
	c, out := token(with("grant_type", "authorization_code", "code", loc.Query().Get("code"), "code_verifier", verifier))
	if c != 200 || out["access_token"] != s.Token() || out["refresh_token"] != "demo-refresh-1" || out["expires_in"] != float64(3600) {
		t.Fatalf("exchange: %d %v", c, out)
	}
	c, out = token(with("grant_type", "refresh_token", "refresh_token", "demo-refresh-1"))
	if c != 200 || out["refresh_token"] != "demo-refresh-2" {
		t.Fatalf("refresh: %d %v", c, out)
	}
	if c, _ := token(with("grant_type", "refresh_token", "refresh_token", "demo-refresh-1")); c != 400 {
		t.Fatalf("the old refresh token must be dead: %d", c)
	}
	if c, _ := token(url.Values{"grant_type": {"refresh_token"}, "client_id": {s.opt.ClientID}, "client_secret": {"nope"}, "refresh_token": {"demo-refresh-2"}}); c != 401 {
		t.Fatalf("wrong client secret: %d", c)
	}
	if c := do(s, "GET", "/oauth/authorize?client_id=x&redirect_uri=http://insecure.example/cb&state=s", "", nil).Code; c != http.StatusBadRequest {
		t.Fatalf("insecure redirect: %d", c)
	}
}

func TestCanonicalSortedQueryEscapesSeparators(t *testing.T) {
	q := url.Values{"b": {"x&y=z%"}, "a": {"1"}, "hmac": {"ignored"}}
	if got, want := CanonicalSortedQuery(q, "hmac"), "a=1&b=x%26y%3Dz%25"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestHostileModes(t *testing.T) {
	s := New(Options{})
	s.Hostile("foreign_links")
	w := do(s, "GET", "/api/products?limit=2", "", authed(s))
	if !strings.Contains(w.Body.String(), "evil.example") || !strings.Contains(w.Body.String(), "../../etc/passwd") {
		t.Fatalf("foreign links absent: %s", w.Body)
	}
	e := New(Options{})
	e.Hostile("echo_headers")
	if w := do(e, "GET", "/api/products?limit=1", "", authed(e)); !strings.Contains(w.Body.String(), e.Token()) {
		t.Fatal("echo_headers must echo the token")
	}
	n := New(Options{})
	n.Hostile("endless_pages")
	var page struct {
		Next string `json:"next_page_token"`
	}
	_ = json.Unmarshal(do(n, "GET", "/api/products?page_token=100000&limit=5", "", authed(n)).Body.Bytes(), &page)
	if page.Next != "100005" {
		t.Fatalf("endless pages ended: %q", page.Next)
	}
	h := New(Options{})
	h.Hostile("huge_body")
	if l := do(h, "GET", "/api/products", "", authed(h)).Body.Len(); l < 8<<20 {
		t.Fatalf("huge body is %d bytes", l)
	}
}
