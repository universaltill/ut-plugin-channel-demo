package app

import (
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/sdk/plugin/channel"
)

const importCursor = "import.products"

type apiProductsPage struct {
	Products []apiProduct `json:"products"`
	Next     string       `json:"next_page_token"`
}

type apiProduct struct {
	ID           string       `json:"id"`
	Title        string       `json:"title"`
	Description  string       `json:"description"`
	Status       string       `json:"status"`
	UpdatedAt    string       `json:"updated_at"`
	CategoryPath []string     `json:"category_path"`
	Variants     []apiVariant `json:"variants"`
	Images       []struct {
		ID       string `json:"id"`
		URL      string `json:"url"`
		Position int    `json:"position"`
	} `json:"images"`
}

type apiVariant struct {
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

// importCatalogue pulls the shop's products page by page and submits them as
// listings (with the channel's stock as observations). It is resumable: the
// next page token is stored after every submitted page, so a failed or
// budget-limited ask continues, never restarts. Records are deterministic
// (external_version is the channel's updated_at), so a second run is
// byte-identical and the host reports zero changes.
func importCatalogue(channel.Event) (any, error) {
	next := cursor(importCursor, cursorRe)
	var imported, skipped int64
	for pages := 0; pages < maxProductPages; pages++ {
		var page apiProductsPage
		q := "/api/products?limit=" + strconv.Itoa(productPage)
		if next != "" {
			q += "&page_token=" + url.QueryEscape(next)
		}
		if err := getJSON(q, &page); err != nil {
			return nil, err
		}
		listings, observed := make([]channel.Listing, 0, len(page.Products)), make([]channel.StockObserved, 0, len(page.Products))
		for _, p := range page.Products {
			l, obs, ok := mapProduct(p)
			if !ok {
				skipped++
				continue
			}
			listings = append(listings, l)
			observed = append(observed, obs...)
		}
		if len(listings) > 0 {
			if _, err := submit(channel.KindListing, listings); err != nil {
				return nil, err
			}
			if len(observed) > 0 {
				if _, err := submitInBatches(channel.KindStockObserved, observed); err != nil {
					return nil, err
				}
			}
			imported += int64(len(listings))
		}
		next = page.Next
		if next != "" && !cursorRe.MatchString(next) {
			return nil, errors.New("channel sent a page token that is not a token")
		}
		if err := channel.CursorSet(importCursor, []byte(next)); err != nil {
			return nil, err
		}
		channel.Log(channel.LevelInfo, "import.page", map[string]any{"records": len(listings), "skipped": skipped})
		if next == "" {
			return map[string]any{"done": true, "imported": imported, "skipped": skipped}, nil
		}
	}
	return map[string]any{"done": false, "imported": imported, "skipped": skipped}, nil
}

func submitInBatches(kind string, recs []channel.StockObserved) (int, error) {
	total := 0
	for len(recs) > 0 {
		n := len(recs)
		if n > channel.MaxBatchRecords {
			n = channel.MaxBatchRecords
		}
		r, err := submit(kind, recs[:n])
		if err != nil {
			return total, err
		}
		total += r
		recs = recs[n:]
	}
	return total, nil
}

// mapProduct turns the channel's product into the contract's listing. A
// product whose ids could not be used safely later is skipped, not repaired.
func mapProduct(p apiProduct) (channel.Listing, []channel.StockObserved, bool) {
	if !okID(p.ID) || !timeRe.MatchString(p.UpdatedAt) {
		return channel.Listing{}, nil, false
	}
	l := channel.Listing{
		Identity: channel.Identity{ExternalID: p.ID, ExternalVersion: p.UpdatedAt},
		Title:    clip(p.Title, 255), Description: clip(p.Description, 5000), Status: listingStatus(p.Status),
		CategoryPath: p.CategoryPath,
	}
	var obs []channel.StockObserved
	for _, v := range p.Variants {
		if !okID(v.ID) || len(v.Currency) != 3 || v.PriceMinor < 0 {
			return channel.Listing{}, nil, false
		}
		out := channel.Variant{
			ExternalID: v.ID, SKU: clip(v.SKU, 100), Barcodes: v.Barcodes, Options: v.Options, PriceMinor: v.PriceMinor,
			Currency: v.Currency, TaxIncluded: v.TaxIncluded, Tracked: v.Tracked,
		}
		if v.CompareAtMinor > v.PriceMinor {
			out.CompareAtMinor = v.CompareAtMinor
		}
		l.Variants = append(l.Variants, out)
		if v.Tracked && v.Available >= 0 {
			obs = append(obs, observed(v.ID, v.Available, p.UpdatedAt))
		}
	}
	for _, im := range p.Images {
		if okID(im.ID) && imageURLOK(im.URL) {
			l.Images = append(l.Images, channel.Image{ExternalID: im.ID, URL: im.URL, Position: im.Position})
		}
	}
	return l, obs, true
}

func observed(variant string, available int64, version string) channel.StockObserved {
	return channel.StockObserved{
		Identity: channel.Identity{ExternalID: variant + ":loc-1", ExternalVersion: version + ":" + strconv.FormatInt(available, 10)},
		Variant:  variant, Location: "loc-1", Available: available,
	}
}

func listingStatus(s string) string {
	switch s {
	case "active", "draft", "archived":
		return s
	}
	return "draft"
}

// imageURLOK keeps an image only when it is https on the channel's own
// domain. The cloud fetches images later (ADR-0162), so a hostile channel
// must not be able to point that fetch elsewhere through a listing.
func imageURLOK(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || len(raw) > 2000 {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return strings.HasSuffix(h, AccountSuffix) && len(h) > len(AccountSuffix)
}
