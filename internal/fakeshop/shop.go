// Package fakeshop is the invented "demo shop" sales channel: a small,
// deterministic HTTP server with OAuth (code grant, PKCE, rotating refresh
// tokens), products with variants, per-variant inventory, orders (carrying
// customer details, as a real channel does), fulfilment and cancel calls,
// HMAC-signed webhook payloads, throttling and a few hostile modes.
//
// Everything is invented data. It is the server behind the demo adapter's
// tests and behind the conformance suite (contract sales-channel-v1.md
// section 6); every adapter ships its own equivalent for its own channel.
//
// The server speaks only to a caller that presents the token in Header. It
// has NO delete route anywhere: the sales-channel contract has no delete.
package fakeshop

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Header carries the access token on every /api call (manifest
// channel.auth.inject.header).
const Header = "X-Demo-Token"

// WebhookHeader carries the base64 HMAC-SHA256 of a webhook body
// (manifest channel.webhooks.verify.header).
const WebhookHeader = "X-Demo-Hmac"

// Location is the one stock location of the shop.
const Location = "loc-1"

// Base is the time every seeded record is dated from.
var Base = time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)

// Options configure a Shop. Zero values get safe defaults.
type Options struct {
	Account  string // host name, default shop-a.demo-shop.example
	Token    string // access token the /api accepts
	ClientID string
	Secret   string // client secret: signs the OAuth callback and webhooks
	Products int    // number of seeded products, default 50
	// ThrottleEvery answers every Nth /api request 429 (0 = never).
	ThrottleEvery int
}

// Hostile switches the server into misbehaving.
type Hostile struct {
	EndlessPages  bool // /api/products never ends
	ForeignLinks  bool // absolute links to another host; ids with path tricks
	EchoHeaders   bool // titles echo every request header (incl. the token)
	HugeBody      bool // /api/products answers 9 MiB
	SlowStreaming bool // reserved
}

// Variant is one sellable variant.
type Variant struct {
	ID             string            `json:"id"`
	SKU            string            `json:"sku"`
	Barcodes       []string          `json:"barcodes"`
	Options        map[string]string `json:"options"`
	PriceMinor     int64             `json:"price_minor"`
	Currency       string            `json:"currency"`
	CompareAtMinor int64             `json:"compare_at_minor"`
	TaxIncluded    bool              `json:"tax_included"`
	Tracked        bool              `json:"tracked"`
	Available      int64             `json:"available"`
}

// Image is a product image.
type Image struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	Position int    `json:"position"`
}

// Product is a product with its variants.
type Product struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	Status       string    `json:"status"`
	UpdatedAt    string    `json:"updated_at"`
	CategoryPath []string  `json:"category_path"`
	Variants     []Variant `json:"variants"`
	Images       []Image   `json:"images"`
}

// OrderLine is one order line.
type OrderLine struct {
	ID               string `json:"id"`
	VariantID        string `json:"variant_id"`
	SKU              string `json:"sku"`
	Title            string `json:"title"`
	Quantity         int64  `json:"quantity"`
	UnitPriceMinor   int64  `json:"unit_price_minor"`
	TaxBP            int64  `json:"tax_bp"`
	TaxIncluded      bool   `json:"tax_included"`
	DiscountMinor    int64  `json:"discount_minor"`
	RefundedQuantity int64  `json:"refunded_quantity"`
	RestockedQty     int64  `json:"restocked_quantity"`
}

// Customer is what the channel knows of the buyer. The adapter must never
// copy name, email, phone or address into a submitted order.
type Customer struct {
	ID      string `json:"id"`
	Country string `json:"country"`
	Name    string `json:"name"`
	Email   string `json:"email"`
	Phone   string `json:"phone"`
	Address string `json:"address"`
}

// Order is an order as the channel reports it.
type Order struct {
	ID               string      `json:"id"`
	Number           string      `json:"number"`
	PlacedAt         string      `json:"placed_at"`
	UpdatedAt        string      `json:"updated_at"`
	Currency         string      `json:"currency"`
	State            string      `json:"state"` // open | fulfilled | cancelled
	Payment          string      `json:"payment"`
	Lines            []OrderLine `json:"lines"`
	ShippingMinor    int64       `json:"shipping_minor"`
	ShippingTaxBP    int64       `json:"shipping_tax_bp"`
	DiscountMinor    int64       `json:"order_discount_minor"`
	TaxAddedMinor    int64       `json:"tax_added_minor"`
	RefundTotalMinor int64       `json:"refund_total_minor"`
	TotalMinor       int64       `json:"total_minor"`
	Customer         Customer    `json:"customer"`
	Tracking         string      `json:"tracking_number,omitempty"`
}

// Request is one request the server saw.
type Request struct {
	Method string
	Path   string // escaped path
	Query  string
	Host   string
	Header http.Header
}

// Shop is the server. Use Handler to serve it.
type Shop struct {
	mu       sync.Mutex
	opt      Options
	hostile  Hostile
	products []Product
	orders   []Order
	stock    map[string]int64
	reqs     []Request
	writes   int
	apiCalls int
	codes    map[string]codeGrant
	refresh  string
	refreshN int
	idem     map[string]int // Idempotency-Key -> performed count
	acts     map[string]int // "<kind>:<order>" -> times performed on the channel
}

type codeGrant struct{ challenge string }

// New returns a seeded shop.
func New(o Options) *Shop {
	if o.Account == "" {
		o.Account = "shop-a.demo-shop.example"
	}
	if o.Token == "" {
		o.Token = "demo-access-token-1"
	}
	if o.ClientID == "" {
		o.ClientID = "demo-client-id"
	}
	if o.Secret == "" {
		o.Secret = "demo-client-secret"
	}
	if o.Products == 0 {
		o.Products = 50
	}
	s := &Shop{opt: o, stock: map[string]int64{}, codes: map[string]codeGrant{}, idem: map[string]int{}, acts: map[string]int{}}
	for i := 1; i <= o.Products; i++ {
		s.products = append(s.products, seedProduct(i))
	}
	for _, p := range s.products {
		for _, v := range p.Variants {
			s.stock[v.ID] = v.Available
		}
	}
	s.orders = seedOrders(s.products)
	return s
}

func seedProduct(i int) Product {
	id := fmt.Sprintf("p-%04d", i)
	p := Product{
		ID: id, Title: fmt.Sprintf("Demo item %d", i), Description: "Invented plain text",
		Status: "active", UpdatedAt: Base.Add(time.Duration(i) * time.Second).Format(time.RFC3339),
		CategoryPath: []string{"Demo", fmt.Sprintf("Range %d", i%5)},
		Images:       []Image{{ID: fmt.Sprintf("i-%04d", i), URL: fmt.Sprintf("https://cdn.demo-shop.example/%s.jpg", id), Position: 1}},
	}
	for j, size := range []string{"S", "M", "L"}[:1+i%3] {
		p.Variants = append(p.Variants, Variant{
			ID: fmt.Sprintf("v-%04d-%d", i, j+1), SKU: fmt.Sprintf("DEMO-%04d-%s", i, size),
			Barcodes: []string{fmt.Sprintf("50000%08d", i*10+j)}, Options: map[string]string{"Size": size},
			PriceMinor: int64(500 + i*10), Currency: "GBP", CompareAtMinor: int64(700 + i*10),
			TaxIncluded: true, Tracked: true, Available: int64(10 + i%7),
		})
	}
	return p
}

func seedOrders(ps []Product) []Order {
	var out []Order
	for n := 1; n <= 3 && n <= len(ps); n++ {
		v := ps[n-1].Variants[0]
		ts := Base.Add(time.Duration(n) * time.Hour).Format(time.RFC3339)
		o := Order{
			ID: fmt.Sprintf("o-%d", 9000+n), Number: fmt.Sprintf("#%d", 1000+n), PlacedAt: ts, UpdatedAt: ts,
			Currency: "GBP", State: "open", Payment: "paid", ShippingMinor: 399, ShippingTaxBP: 2000,
			Lines: []OrderLine{{ID: fmt.Sprintf("l-%d", n), VariantID: v.ID, SKU: v.SKU, Title: ps[n-1].Title,
				Quantity: int64(n), UnitPriceMinor: v.PriceMinor, TaxBP: 2000, TaxIncluded: true}},
			Customer: Customer{ID: fmt.Sprintf("c-%d", 70+n), Country: "GB", Name: "Invented Person", Email: "invented@example.invalid", Phone: "+440000000000", Address: "1 Invented Street"},
		}
		o.TotalMinor = v.PriceMinor*int64(n) + o.ShippingMinor
		out = append(out, o)
	}
	return out
}

// SetHostile switches hostile modes.
func (s *Shop) SetHostile(h Hostile) {
	s.mu.Lock()
	s.hostile = h
	s.mu.Unlock()
}

// Hostile switches one mode on by the conformance suite's name for it
// (conformance.EndlessPages and the others); it is cumulative.
func (s *Shop) Hostile(mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch mode {
	case "endless_pages":
		s.hostile.EndlessPages = true
	case "foreign_links":
		s.hostile.ForeignLinks = true
	case "echo_headers":
		s.hostile.EchoHeaders = true
	case "huge_body":
		s.hostile.HugeBody = true
	default:
		panic("fakeshop: unknown hostile mode " + mode)
	}
}

// ServeHTTP makes a Shop an http.Handler.
func (s *Shop) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.serve(w, r) }

// Account is the shop's host name.
func (s *Shop) Account() string { return s.opt.Account }

// Token is the access token the /api accepts.
func (s *Shop) Token() string { return s.opt.Token }

// Secret is the client secret (signs the OAuth callback and webhooks).
func (s *Shop) Secret() string { return s.opt.Secret }

// Requests returns a copy of every request seen.
func (s *Shop) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.reqs...)
}

// Writes counts requests that were not GET or HEAD.
func (s *Shop) Writes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes
}

// Performed counts how many times an action ("fulfil", "cancel") actually
// changed an order on the channel.
func (s *Shop) Performed(kind, orderID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acts[kind+":"+orderID]
}

// Stock returns the channel's quantity for a variant.
func (s *Shop) Stock(variant string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stock[variant]
}

// SetStock changes a quantity as if the merchant did it in the channel.
func (s *Shop) SetStock(variant string, n int64) {
	s.mu.Lock()
	s.stock[variant] = n
	s.mu.Unlock()
}

// Orders returns a copy of the orders.
func (s *Shop) Orders() []Order {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Order(nil), s.orders...)
}

// ProductList returns a copy of the products.
func (s *Shop) ProductList() []Product {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Product(nil), s.products...)
}

// Fingerprint is a digest of everything the shop holds. It changes when the
// adapter writes anything (conformance check 1).
func (s *Shop) Fingerprint() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(struct {
		P []Product
		O []Order
		S map[string]int64
	}{s.products, s.orders, s.stock})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// SignWebhook returns the signature header value for a body.
func (s *Shop) SignWebhook(body []byte) string {
	m := hmac.New(sha256.New, []byte(s.opt.Secret))
	m.Write(body)
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// WebhookBody builds an invented webhook payload.
func (s *Shop) WebhookBody(id, topic, resource string) []byte {
	b, _ := json.Marshal(map[string]any{"id": id, "topic": topic, "shop": s.opt.Account, "resource": map[string]string{"id": resource}})
	return b
}

// RedactBody builds an invented customers/redact payload: the customer's id
// as the resource, plus the ids of the orders to erase (the channel's
// equivalent of Shopify's orders_to_redact; ut-docs#4929). No personal data.
func (s *Shop) RedactBody(id, customerID string, orderIDs []string) []byte {
	m := map[string]any{"id": id, "topic": "customers/redact", "shop": s.opt.Account, "resource": map[string]string{"id": customerID}}
	if orderIDs != nil {
		m["orders_to_redact"] = orderIDs
	}
	b, _ := json.Marshal(m)
	return b
}

// CanonicalSortedQuery is the contract's canonical string for
// auth.callback_verify (mode sorted-query-without-param): the parameters
// except skip, sorted by key; each pair enc(key)=enc(value) where enc maps
// "%" to "%25", "&" to "%26", "=" to "%3D"; joined by "&".
func CanonicalSortedQuery(q url.Values, skip string) string {
	enc := strings.NewReplacer("%", "%25", "&", "%26", "=", "%3D")
	keys := make([]string, 0, len(q))
	for k := range q {
		if k != skip {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var pairs []string
	for _, k := range keys {
		pairs = append(pairs, enc.Replace(k)+"="+enc.Replace(q.Get(k)))
	}
	return strings.Join(pairs, "&")
}

// Handler serves the shop.
func (s *Shop) Handler() http.Handler { return http.HandlerFunc(s.serve) }

func (s *Shop) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.reqs = append(s.reqs, Request{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery, Host: r.Host, Header: r.Header.Clone()})
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		s.writes++
	}
	s.mu.Unlock()

	switch {
	case r.URL.Path == "/oauth/authorize":
		s.authorize(w, r)
	case r.URL.Path == "/oauth/token":
		s.token(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/"):
		s.api(w, r)
	default:
		writeJSON(w, 404, map[string]string{"error": "not_found"})
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(b)
}

func (s *Shop) api(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(Header)), []byte(s.opt.Token)) != 1 {
		writeJSON(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	s.mu.Lock()
	s.apiCalls++
	throttled := s.opt.ThrottleEvery > 0 && s.apiCalls%s.opt.ThrottleEvery == 0
	s.mu.Unlock()
	if throttled {
		w.Header().Set("Retry-After", "0")
		writeJSON(w, 429, map[string]string{"error": "throttled"})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/")
	parts := strings.Split(path, "/")
	switch {
	case r.Method == http.MethodGet && path == "shop":
		s.shop(w)
	case r.Method == http.MethodGet && path == "products":
		s.listProducts(w, r)
	case r.Method == http.MethodGet && path == "orders":
		s.listOrders(w, r)
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "orders":
		s.getOrder(w, parts[1])
	case r.Method == http.MethodGet && len(parts) == 3 && parts[0] == "orders" && parts[2] == "contact":
		s.contact(w, parts[1])
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "inventory":
		s.getStock(w, parts[1])
	case r.Method == http.MethodPut && len(parts) == 2 && parts[0] == "inventory":
		s.putStock(w, r, parts[1])
	case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "orders" && parts[2] == "fulfilments":
		s.fulfil(w, r, parts[1])
	case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "orders" && parts[2] == "cancel":
		s.cancel(w, r, parts[1])
	default:
		// Includes every DELETE: there is no delete anywhere.
		writeJSON(w, 405, map[string]string{"error": "method_not_allowed"})
	}
}

func (s *Shop) shop(w http.ResponseWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, 200, map[string]any{
		"name": "Demo Shop A", "scopes": []string{"products", "inventory", "orders"},
		"locations":     []map[string]string{{"id": Location, "name": "Main warehouse"}},
		"product_count": len(s.products), "order_count": len(s.orders),
	})
}

func page(r *http.Request, def, max int) (offset, limit int) {
	limit = def
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n < max {
		limit = n
	}
	offset, _ = strconv.Atoi(r.URL.Query().Get("page_token"))
	if offset < 0 {
		offset = 0
	}
	return
}

func (s *Shop) listProducts(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	off, lim := page(r, 20, 51)
	if s.hostile.HugeBody {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"products":[],"pad":"` + strings.Repeat("x", 9<<20) + `"}`))
		return
	}
	var out []Product
	next := ""
	if s.hostile.EndlessPages {
		for i := 0; i < lim; i++ {
			out = append(out, seedProduct(100000+off+i))
		}
		next = strconv.Itoa(off + lim)
	} else {
		end := off + lim
		if end > len(s.products) {
			end = len(s.products)
		}
		if off < len(s.products) {
			out = append(out, s.products[off:end]...)
		}
		if end < len(s.products) {
			next = strconv.Itoa(end)
		}
	}
	for i := range out {
		out[i] = s.hostileProduct(out[i], r)
		for j := range out[i].Variants {
			if q, ok := s.stock[out[i].Variants[j].ID]; ok {
				out[i].Variants[j].Available = q
			}
		}
	}
	resp := map[string]any{"products": out, "next_page_token": next}
	if s.hostile.ForeignLinks && next != "" {
		resp["next_url"] = "https://evil.example/api/products?page_token=" + next
	}
	writeJSON(w, 200, resp)
}

func (s *Shop) hostileProduct(p Product, r *http.Request) Product {
	if s.hostile.EchoHeaders {
		var sb strings.Builder
		for k, v := range r.Header {
			sb.WriteString(k + "=" + strings.Join(v, ",") + ";")
		}
		p.Title = sb.String()
	}
	if s.hostile.ForeignLinks {
		p.ID = "../../etc/passwd?x=1#" + p.ID
		p.Images = []Image{{ID: "i-evil", URL: "https://evil.example/steal.jpg", Position: 1}}
	}
	return p
}

func (s *Shop) listOrders(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	since := r.URL.Query().Get("updated_since")
	var all []Order
	for _, o := range s.orders {
		if since == "" || o.UpdatedAt >= since {
			all = append(all, o)
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].UpdatedAt < all[j].UpdatedAt })
	off, lim := page(r, 20, 51)
	end := off + lim
	if end > len(all) {
		end = len(all)
	}
	var out []Order
	if off < len(all) {
		out = all[off:end]
	}
	next := ""
	if end < len(all) {
		next = strconv.Itoa(end)
	}
	writeJSON(w, 200, map[string]any{"orders": out, "next_page_token": next})
}

func (s *Shop) order(id string) *Order {
	for i := range s.orders {
		if s.orders[i].ID == id {
			return &s.orders[i]
		}
	}
	return nil
}

func (s *Shop) getOrder(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.order(id); o != nil {
		writeJSON(w, 200, o)
		return
	}
	writeJSON(w, 404, map[string]string{"error": "not_found"})
}

func (s *Shop) contact(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.order(id)
	if o == nil {
		writeJSON(w, 404, map[string]string{"error": "not_found"})
		return
	}
	writeJSON(w, 200, map[string]string{"name": o.Customer.Name, "email": o.Customer.Email, "phone": o.Customer.Phone, "address": o.Customer.Address})
}

func (s *Shop) getStock(w http.ResponseWriter, variant string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.stock[variant]
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "not_found"})
		return
	}
	writeJSON(w, 200, map[string]any{"variant": variant, "location": Location, "available": q})
}

func (s *Shop) putStock(w http.ResponseWriter, r *http.Request, variant string) {
	var in struct {
		Available *int64 `json:"available"`
		Expect    *int64 `json:"expect"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in) != nil || in.Available == nil || *in.Available < 0 {
		writeJSON(w, 422, map[string]string{"error": "invalid"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.stock[variant]
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "not_found"})
		return
	}
	if in.Expect != nil && *in.Expect != cur {
		writeJSON(w, 409, map[string]any{"error": "stale", "available": cur})
		return
	}
	s.stock[variant] = *in.Available
	writeJSON(w, 200, map[string]any{"variant": variant, "location": Location, "available": *in.Available})
}

func (s *Shop) fulfil(w http.ResponseWriter, r *http.Request, id string) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeJSON(w, 400, map[string]string{"error": "idempotency_key_required"})
		return
	}
	var in struct {
		Carrier  string `json:"carrier"`
		Tracking string `json:"tracking_number"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in)
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.order(id)
	if o == nil {
		writeJSON(w, 404, map[string]string{"error": "not_found"})
		return
	}
	if s.idem[key] > 0 { // a replay of the same request: same answer, no second effect
		writeJSON(w, 200, o)
		return
	}
	s.idem[key]++
	if o.State == "fulfilled" {
		writeJSON(w, 409, map[string]string{"error": "already_fulfilled"})
		return
	}
	if o.State == "cancelled" {
		writeJSON(w, 409, map[string]string{"error": "order_cancelled"})
		return
	}
	o.State, o.Tracking = "fulfilled", in.Tracking
	o.UpdatedAt = s.tick()
	s.acts["fulfil:"+id]++
	writeJSON(w, 200, o)
}

func (s *Shop) cancel(w http.ResponseWriter, r *http.Request, id string) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeJSON(w, 400, map[string]string{"error": "idempotency_key_required"})
		return
	}
	var in struct {
		Reason  string `json:"reason"`
		Restock bool   `json:"restock"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in)
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.order(id)
	if o == nil {
		writeJSON(w, 404, map[string]string{"error": "not_found"})
		return
	}
	if s.idem[key] > 0 {
		writeJSON(w, 200, o)
		return
	}
	s.idem[key]++
	if o.State == "cancelled" {
		writeJSON(w, 409, map[string]string{"error": "already_cancelled"})
		return
	}
	if o.State == "fulfilled" {
		writeJSON(w, 409, map[string]string{"error": "already_fulfilled"})
		return
	}
	o.State = "cancelled"
	o.UpdatedAt = s.tick()
	s.acts["cancel:"+id]++
	writeJSON(w, 200, o)
}

// tick returns a timestamp one minute after the newest order update: the
// shop's clock only moves forward, as a real channel's updated_at does.
func (s *Shop) tick() string {
	newest := Base
	for _, o := range s.orders {
		if t, err := time.Parse(time.RFC3339, o.UpdatedAt); err == nil && t.After(newest) {
			newest = t
		}
	}
	return newest.Add(time.Minute).UTC().Format(time.RFC3339)
}

// FulfilOutsideUT marks an order fulfilled as the merchant would in the
// channel's own admin, so a later fulfilment.create from UT meets
// "already fulfilled".
func (s *Shop) FulfilOutsideUT(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.order(id); o != nil {
		o.State = "fulfilled"
		o.UpdatedAt = s.tick()
	}
}

// CancelOutsideUT cancels an order in the channel's own admin.
func (s *Shop) CancelOutsideUT(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.order(id); o != nil {
		o.State = "cancelled"
		o.UpdatedAt = s.tick()
	}
}
