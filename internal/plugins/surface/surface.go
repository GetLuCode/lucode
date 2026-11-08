// Package surface serves a plugin's frontend: the script the browser runs in a Worker of its own,
// and the short-lived tickets that let the browser load it. A Worker is loaded by URL, without the
// page's Authorization header, so the page asks for a ticket with its token first and the Worker
// loads the script with the ticket. The response's Content-Security-Policy is what cuts the Worker
// off from the network; the page checks everything the Worker sends before it draws it.
package surface

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

//go:embed worker.js
var runtimeJS string

// RuntimeJS is the start of every frontend, with __LUCODE_CONTEXT__ where the context goes. The
// CLI runs it too, to test a page without a browser.
func RuntimeJS() string { return runtimeJS }

// CSP of the script's response. A dedicated Worker follows the policy of its own response: no
// fetch, no WebSocket, no importScripts or import(), no nested workers, no eval.
const CSP = "default-src 'none'"

// Context is what a frontend knows about where it runs, as lucode.context.
type Context struct {
	PluginID      string `json:"pluginId"`
	PluginName    string `json:"pluginName"`
	PluginVersion string `json:"pluginVersion"`
	PageID        string `json:"pageId"`
	ProjectID     string `json:"projectId"`
}

// Script is the runtime with its context, then the plugin's bundle.
func Script(c Context, bundle []byte) []byte {
	raw, _ := json.Marshal(c) // escapes <, > and &, and the fields are plain strings
	var b strings.Builder
	b.Grow(len(runtimeJS) + len(bundle) + 256)
	b.WriteString(strings.Replace(runtimeJS, "__LUCODE_CONTEXT__", string(raw), 1))
	b.WriteString("\n;\n")
	b.Write(bundle)
	b.WriteString("\n")
	return []byte(b.String())
}

// TicketTTL is how long a ticket waits for its Worker.
const TicketTTL = 60 * time.Second

// Claims are what a ticket stands for.
type Claims struct {
	PluginID  string `json:"p"`
	PageID    string `json:"g"`
	ProjectID string `json:"j"`
	Expires   int64  `json:"e"`
	Nonce     string `json:"n"`
}

// Tickets mints and redeems tickets. Each one works once, before it expires, and only in the
// process that made it: the key is random and lives in memory.
type Tickets struct {
	key  []byte
	now  func() time.Time
	mu   sync.Mutex
	used map[string]int64 // nonce -> expiry
}

func NewTickets() *Tickets {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	return &Tickets{key: key, now: time.Now, used: map[string]int64{}}
}

var ErrTicket = errors.New("this page's ticket is not valid any more: open the page again")

func (t *Tickets) sign(payload string) string {
	m := hmac.New(sha256.New, t.key)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Mint makes a ticket for one load of a page's script.
func (t *Tickets) Mint(pluginID, pageID, projectID string) string {
	nonce := make([]byte, 12)
	_, _ = rand.Read(nonce)
	c := Claims{PluginID: pluginID, PageID: pageID, ProjectID: projectID,
		Expires: t.now().Add(TicketTTL).Unix(), Nonce: base64.RawURLEncoding.EncodeToString(nonce)}
	raw, _ := json.Marshal(c)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	return payload + "." + t.sign(payload)
}

// Redeem checks a ticket and spends it.
func (t *Tickets) Redeem(ticket string) (Claims, error) {
	var c Claims
	payload, sig, ok := strings.Cut(ticket, ".")
	if !ok || len(ticket) > 2048 || !hmac.Equal([]byte(sig), []byte(t.sign(payload))) {
		return c, ErrTicket
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || json.Unmarshal(raw, &c) != nil {
		return c, ErrTicket
	}
	now := t.now().Unix()
	if c.Expires < now || c.Nonce == "" {
		return c, ErrTicket
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for n, exp := range t.used {
		if exp < now {
			delete(t.used, n)
		}
	}
	if _, spent := t.used[c.Nonce]; spent {
		return c, ErrTicket
	}
	t.used[c.Nonce] = c.Expires
	return c, nil
}
