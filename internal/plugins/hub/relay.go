package hub

import (
	"context"
	"log"
	"time"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/plugins"

	"github.com/dop251/goja"
)

// Webhooks. A plugin asks the hub's relay client for an address (lucode.relay.createEndpoint), gives it
// to a service (Telegram's setWebhook), and what the service posts comes back as the plugin's
// events.webhook, in the project the endpoint was made for. The relay client lives on the hub only:
// a runner never sees it.

const (
	maxRelayEndpoints = 10  // addresses one plugin may hold
	maxRelayLabel     = 100 // characters
)

func relayAllowed(l *loadedPlugin, provider string) bool {
	if l.manifest.Targets.Server == nil {
		return false
	}
	eff := plugins.Effective(plugins.TargetServer, l.manifest.Targets.Server.Permissions, l.row.Granted)
	if eff.Relay == nil {
		return false
	}
	for _, name := range eff.Relay.Providers {
		if name == provider {
			return true
		}
	}
	return false
}

func (h *Host) relayModule(l *loadedPlugin, p plugins.Permissions) plugins.Module {
	pluginID := l.row.ID
	granted := func(s *plugins.Scope) {
		need(s, p.Relay != nil, "relay")
	}
	forProvider := func(s *plugins.Scope, provider string) {
		granted(s)
		for _, name := range p.Relay.Providers {
			if name == provider {
				return
			}
		}
		s.Throw("this plugin was not granted the relay permission for %s", provider)
	}
	client := func(s *plugins.Scope) plugins.RelayClient {
		if h.deps.Relay == nil {
			s.Throw("this hub has no webhook relay")
		}
		return h.deps.Relay
	}
	row := func(r dbpkg.RelayEndpointRow) map[string]any {
		return map[string]any{"id": r.EndpointID, "provider": r.Provider, "label": r.Label, "url": r.URL, "projectId": r.ProjectID, "created": r.CreatedAt}
	}
	return plugins.Module{Name: "relay", Funcs: map[string]plugins.Func{
		"status": func(s *plugins.Scope, _ goja.FunctionCall) goja.Value {
			granted(s)
			if h.deps.Relay == nil {
				return s.JS(map[string]any{"configured": false, "connected": false, "url": ""})
			}
			st := h.deps.Relay.Status()
			return s.JS(map[string]any{"configured": st.Configured, "connected": st.Connected, "url": st.URL})
		},
		"createEndpoint": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			o := s.Obj(c, 0)
			provider, _ := o["provider"].(string)
			forProvider(s, provider)
			label, _ := o["label"].(string)
			if r := []rune(label); len(r) > maxRelayLabel {
				label = string(r[:maxRelayLabel])
			}
			projectID := project(s) // its events belong to this project
			mine, err := dbpkg.ListRelayEndpoints(h.deps.DB, pluginID)
			if err != nil {
				s.Throw("the endpoints could not be read")
			}
			if len(mine) >= maxRelayEndpoints {
				s.Throw("a plugin can hold at most %d webhook endpoints: delete one first", maxRelayEndpoints)
			}
			cl := client(s)
			ep, err := cl.CreateEndpoint(s.Ctx, provider, label)
			if err != nil {
				s.Throw("%s", err.Error())
			}
			rec := dbpkg.RelayEndpointRow{EndpointID: ep.ID, PluginID: pluginID, Provider: provider, ProjectID: projectID, Label: label, URL: ep.URL}
			if err := dbpkg.InsertRelayEndpoint(h.deps.DB, rec); err != nil {
				// an address nobody owns would only collect events: give it back
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = cl.DeleteEndpoint(ctx, ep.ID)
				s.Throw("the endpoint could not be saved")
			}
			addSecret(l, ep.Secret) // a log or a result never shows it
			out := row(rec)
			out["secret"] = ep.Secret // shown once: the plugin gives it to the service
			return s.JS(out)
		},
		"listEndpoints": func(s *plugins.Scope, _ goja.FunctionCall) goja.Value {
			granted(s)
			rows, err := dbpkg.ListRelayEndpoints(h.deps.DB, pluginID)
			if err != nil {
				s.Throw("the endpoints could not be read")
			}
			out := make([]any, 0, len(rows))
			for _, r := range rows {
				out = append(out, row(r))
			}
			return s.JS(out)
		},
		"deleteEndpoint": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			granted(s)
			id := s.Str(c, 0, "id")
			r, err := dbpkg.GetRelayEndpoint(h.deps.DB, id)
			if err != nil || r.PluginID != pluginID {
				s.Throw("no such webhook endpoint") // the same words for another plugin's, which is not for it to know
			}
			if err := client(s).DeleteEndpoint(s.Ctx, id); err != nil {
				s.Throw("%s", err.Error())
			}
			if _, err := dbpkg.DeleteRelayEndpoint(h.deps.DB, pluginID, id); err != nil {
				s.Throw("the endpoint could not be removed")
			}
			return goja.Undefined()
		},
	}}
}

// addSecret keeps a value out of anything that leaves the hub, until the plugin's settings are next
// read again. The relay's secret for an address is only in memory, so only in memory is it scrubbed.
func addSecret(l *loadedPlugin, v string) {
	if len(v) < 4 {
		return
	}
	list, _ := l.secrets.Load().([]string)
	l.secrets.Store(append(append([]string{}, list...), v))
}

// deliverWebhook hands a webhook from the relay to the plugin that owns the address. The relay client
// acknowledges the event when this returns, so it waits for the handler: a plugin that throws still
// has the event acknowledged (it would only come again), and one that is off or gone drops it.
func (h *Host) deliverWebhook(_ context.Context, ev plugins.RelayEvent) {
	r, err := dbpkg.GetRelayEndpoint(h.deps.DB, ev.Endpoint)
	if err != nil {
		log.Printf("[plugins] a webhook for an address no plugin owns (%.6s): dropped", ev.Endpoint)
		return
	}
	h.mu.RLock()
	l := h.loaded[r.PluginID]
	h.mu.RUnlock()
	switch {
	case l == nil || l.rt == nil || !l.row.Enabled || l.manifest.Targets.Server == nil:
		log.Printf("[plugins] %s is off: a webhook for it was dropped", r.PluginID)
		return
	case ev.Provider != r.Provider:
		log.Printf("[plugins] %s: a webhook came as %s for an address made for %s: dropped", r.PluginID, ev.Provider, r.Provider)
		return
	case !relayAllowed(l, r.Provider):
		log.Printf("[plugins] %s may not get %s webhooks: dropped", r.PluginID, r.Provider)
		return
	}
	h.fireEventWait(l, "webhook", r.ProjectID, map[string]any{
		"endpoint":    ev.Endpoint,
		"provider":    r.Provider,
		"label":       r.Label,
		"projectId":   r.ProjectID,
		"receivedAt":  ev.ReceivedAt,
		"contentType": ev.ContentType,
		"body":        ev.Body,
	}, 0)
	// The hub tells every open browser that a webhook arrived, over the websocket it already has,
	// so a page of the plugin shows it at once. The body stays out of it: what anyone writes to a bot
	// is not pushed raw to every browser, and the plugin decides how its page shows it.
	if h.deps.Services != nil {
		h.deps.Services.Broadcast("", map[string]any{
			"type":       "plugin_webhook",
			"pluginId":   r.PluginID,
			"provider":   r.Provider,
			"endpoint":   ev.Endpoint,
			"projectId":  r.ProjectID,
			"receivedAt": ev.ReceivedAt,
		})
	}
}

// forgetEndpoints gives a removed plugin's addresses back to the relay. It runs away from the caller:
// the relay may be down, and an address that stays there only holds events nobody reads.
func (h *Host) forgetEndpoints(rows []dbpkg.RelayEndpointRow) {
	if h.deps.Relay == nil || len(rows) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, r := range rows {
			if err := h.deps.Relay.DeleteEndpoint(ctx, r.EndpointID); err != nil {
				log.Printf("[plugins] could not give back the webhook address of %s: %v", r.PluginID, err)
			}
		}
	}()
}
