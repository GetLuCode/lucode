package api

import "testing"

func TestParseAnthropicProfile(t *testing.T) {
	body := []byte(`{"account":{"email":"a@b.dev","full_name":"Ana Perez","display_name":""},"organization":{"name":"Acme","organization_type":"claude_max","rate_limit_tier":"tier4"}}`)
	email, display, org, plan := parseAnthropicProfile(body)
	if email != "a@b.dev" || display != "Ana Perez" || org != "Acme" || plan != "claude_max" {
		t.Errorf("got %q %q %q %q", email, display, org, plan)
	}
	if e, _, _, _ := parseAnthropicProfile([]byte(`not json`)); e != "" {
		t.Error("garbage must give empty fields")
	}
}
