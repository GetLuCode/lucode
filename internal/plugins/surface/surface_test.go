package surface

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTicketsWorkOnceAndExpire(t *testing.T) {
	tk := NewTickets()
	now := time.Unix(1_000_000, 0)
	tk.now = func() time.Time { return now }

	ticket := tk.Mint("acme.x", "main", "p1")
	c, err := tk.Redeem(ticket)
	if err != nil || c.PluginID != "acme.x" || c.PageID != "main" || c.ProjectID != "p1" {
		t.Fatalf("redeem: %+v %v", c, err)
	}
	if _, err := tk.Redeem(ticket); err != ErrTicket {
		t.Fatalf("a second use: %v", err)
	}

	late := tk.Mint("acme.x", "main", "p1")
	now = now.Add(TicketTTL + time.Second)
	if _, err := tk.Redeem(late); err != ErrTicket {
		t.Fatalf("an expired ticket: %v", err)
	}

	// a ticket of another process, or one with its claims changed, is nothing
	other := NewTickets().Mint("acme.x", "main", "p1")
	if _, err := tk.Redeem(other); err != ErrTicket {
		t.Fatalf("another key: %v", err)
	}
	fresh := tk.Mint("acme.x", "main", "p1")
	payload, sig, _ := strings.Cut(fresh, ".")
	forged := strings.Replace(payload, "a", "b", 1) + "." + sig
	if _, err := tk.Redeem(forged); err != ErrTicket {
		t.Fatalf("changed claims: %v", err)
	}
	for _, junk := range []string{"", ".", "x.y", strings.Repeat("a", 3000)} {
		if _, err := tk.Redeem(junk); err != ErrTicket {
			t.Fatalf("%q: %v", junk, err)
		}
	}
}

func TestScriptPutsTheRuntimeFirst(t *testing.T) {
	out := string(Script(Context{PluginID: "acme.x", PluginName: "</script><b>", PageID: "main", ProjectID: "p1"}, []byte("BUNDLE()")))
	if strings.Contains(out, "__LUCODE_CONTEXT__") {
		t.Fatal("the context placeholder is left")
	}
	escaped, _ := json.Marshal("</script><b>")
	if !strings.Contains(out, `"pluginName":`+string(escaped)) || strings.Contains(out, "</script>") || !strings.Contains(string(escaped), "u003c") {
		t.Fatalf("the context is JSON with <, > escaped:\n%s", out[strings.Index(out, "})(self"):])
	}
	if strings.Index(out, "BUNDLE()") < strings.Index(out, `"projectId":"p1"`) {
		t.Fatal("the plugin's bundle must run after the runtime")
	}
}
