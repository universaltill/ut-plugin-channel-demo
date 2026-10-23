// Package app is the demo channel adapter: it speaks the demo shop's REST
// API through the host's one network path (channel.HTTP) and hands records to
// the host with channel.Submit. It holds no credential, no account name and
// no state of its own beyond the host's cursors.
package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"

	"github.com/universaltill/universal-till/sdk/plugin/channel"
)

// base is the manifest's own placeholder for the connection's verified
// account host. A plugin is never told its account: the host fills it in
// before the egress rules run (ut-cloud channelhost expandAccount).
const base = "https://{account}"

// AccountSuffix is the manifest's channel.account_suffix; validate.sh keeps
// the two equal.
const AccountSuffix = ".demo-shop.example"

var (
	accountRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.demo-shop\.example$`)
	// idRe is what an id must look like to be put in a URL path. No slash,
	// no dot-only segment, no query or fragment character.
	idRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	cursorRe = regexp.MustCompile(`^[0-9A-Za-z_-]{1,64}$`)
	timeRe   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)
)

// Per-ask budgets, well inside the host's caps (2,000 HTTP calls and 20,000
// records per call, contract section 3.1).
const (
	productPage     = 20
	maxProductPages = 40 // per import ask; the rest resumes from the cursor
	orderPage       = 50
	maxOrderPages   = 20 // per tick
)

// statusError is a non-2xx answer. It carries the status and the channel's
// short error code only, never a body: bodies may hold personal data.
type statusError struct {
	Status int
	Code   string
}

func (e *statusError) Error() string { return fmt.Sprintf("channel answered %d %s", e.Status, e.Code) }

// call sends one request and returns status, raw body.
func call(method, path string, headers map[string]string, body any) (int, []byte, error) {
	req := channel.HTTPRequest{Method: method, URL: base + path, Headers: headers}
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		req.Body = b
		if req.Headers == nil {
			req.Headers = map[string]string{}
		}
		req.Headers["Content-Type"] = "application/json"
	}
	resp, err := channel.HTTP(req)
	if err != nil {
		return 0, nil, err
	}
	return resp.Status, resp.Body, nil
}

func errCode(raw []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	switch e.Error {
	case "stale", "already_fulfilled", "already_cancelled", "order_cancelled", "not_found", "invalid", "unauthorized":
		return e.Error
	}
	return ""
}

// getJSON GETs path and decodes a 200 answer into v.
func getJSON(path string, v any) error {
	st, raw, err := call("GET", path, nil, nil)
	if err != nil {
		return err
	}
	if st != 200 {
		return &statusError{Status: st, Code: errCode(raw)}
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return errors.New("channel answered something that is not the documented JSON")
	}
	return nil
}

func okID(id string) bool { return idRe.MatchString(id) }

func esc(id string) string { return url.PathEscape(id) }

// cursor reads a cursor key; a missing or malformed one is "".
func cursor(key string, re *regexp.Regexp) string {
	b, err := channel.CursorGet(key)
	if err != nil || !re.Match(b) {
		return ""
	}
	return string(b)
}

// submit hands a batch to the host and reports how many it rejected. Reasons
// are logged by code only.
func submit(kind string, records any) (rejected int, err error) {
	res, err := channel.Submit(kind, records)
	if err != nil {
		return 0, err
	}
	for _, r := range res {
		if r.Status == channel.ResultRejected {
			rejected++
		}
	}
	if rejected > 0 {
		channel.Log(channel.LevelWarn, "submit.rejected", map[string]any{"kind": kind, "rejected": rejected, "of": len(res)})
	}
	return rejected, nil
}
