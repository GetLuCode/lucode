package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/plugins"

	"github.com/dop251/goja"
)

// The server target's SDK. The plugin's identity comes from the host in every call, and a project
// from the invocation, so one plugin cannot name another's data or another project's board.

const (
	maxEmitBytes = 64 << 10
	maxEmits     = 50 // events one invocation can fire
)

func (h *Host) serverModules(l *loadedPlugin, p plugins.Permissions) []plugins.Module {
	return []plugins.Module{
		h.settingsModule(l),
		h.storageModule(l, p),
		h.conversationsModule(l, p),
		h.sessionsModule(p),
		h.boardModule(l, p),
		h.flowsModule(l, p),
		h.browserModule(l, p),
		h.runnersModule(l),
		h.relayModule(l, p),
	}
}

func need(s *plugins.Scope, ok bool, perm string) {
	if !ok {
		s.Throw("this needs the %s permission, which this plugin was not granted", perm)
	}
}

func project(s *plugins.Scope) string {
	if s.Inv.Context.ProjectID == "" {
		s.Throw("this call has no project")
	}
	return s.Inv.Context.ProjectID
}

// ---- settings ----

func (h *Host) settingsModule(l *loadedPlugin) plugins.Module {
	read := func(def plugins.SettingDef, stored map[string]string) any {
		raw, ok := stored[def.Key]
		if !ok {
			return def.Default
		}
		var v any
		_ = json.Unmarshal([]byte(raw), &v)
		return v
	}
	return plugins.Module{Name: "settings", Funcs: map[string]plugins.Func{
		"get": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			key := s.Str(c, 0, "key")
			stored, err := dbpkg.PluginSettings(h.deps.DB, l.row.ID)
			if err != nil {
				s.Throw("the settings could not be read")
			}
			for _, def := range l.manifest.Contributes.Settings {
				if def.Key == key {
					return s.JS(read(def, stored))
				}
			}
			return goja.Null()
		},
		"all": func(s *plugins.Scope, _ goja.FunctionCall) goja.Value {
			stored, err := dbpkg.PluginSettings(h.deps.DB, l.row.ID)
			if err != nil {
				s.Throw("the settings could not be read")
			}
			out := map[string]any{}
			for _, def := range l.manifest.Contributes.Settings {
				out[def.Key] = read(def, stored)
			}
			return s.JS(out)
		},
	}}
}

// ---- storage ----

func (h *Host) storageModule(l *loadedPlugin, p plugins.Permissions) plugins.Module {
	id := l.row.ID
	return plugins.Module{Name: "storage", Funcs: map[string]plugins.Func{
		"get": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			need(s, p.Storage, "storage")
			raw, found, err := dbpkg.PluginStorageGet(h.deps.DB, id, s.Str(c, 0, "key"))
			if err != nil {
				s.Throw("the storage could not be read")
			}
			if !found {
				return goja.Null()
			}
			var v any
			if json.Unmarshal([]byte(raw), &v) != nil {
				return goja.Null()
			}
			return s.JS(v)
		},
		"set": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			need(s, p.Storage, "storage")
			key := s.Str(c, 0, "key")
			raw, err := json.Marshal(s.Plain(c.Argument(1)))
			if err != nil {
				s.Throw("this value cannot be stored")
			}
			if err := dbpkg.PluginStorageSet(h.deps.DB, id, key, string(raw)); err != nil {
				if errors.Is(err, dbpkg.ErrPluginQuota) {
					s.Throw("%s", err.Error())
				}
				s.Throw("the storage could not be written")
			}
			return goja.Undefined()
		},
		"delete": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			need(s, p.Storage, "storage")
			if err := dbpkg.PluginStorageDelete(h.deps.DB, id, s.Str(c, 0, "key")); err != nil {
				s.Throw("the storage could not be written")
			}
			return goja.Undefined()
		},
		"list": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			need(s, p.Storage, "storage")
			prefix := ""
			if a := c.Argument(0); !goja.IsUndefined(a) && !goja.IsNull(a) {
				prefix = a.String()
			}
			keys, err := dbpkg.PluginStorageList(h.deps.DB, id, prefix)
			if err != nil {
				s.Throw("the storage could not be read")
			}
			return s.JS(keys)
		},
	}}
}

// ---- conversations ----

func convPlain(c dbpkg.PluginConversation) map[string]any {
	raw, _ := json.Marshal(c)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

func (h *Host) conversationsModule(l *loadedPlugin, p plugins.Permissions) plugins.Module {
	id := l.row.ID
	// a session a plugin names must be of the project it runs in
	checkSession := func(s *plugins.Scope, sessionID string) dbpkg.Session {
		sess, err := dbpkg.GetSession(h.deps.DB, sessionID)
		if err != nil || sess.ProjectID != project(s) {
			s.Throw("no session %q in this project", sessionID)
		}
		return sess
	}
	return plugins.Module{Name: "conversations", Funcs: map[string]plugins.Func{
		"get": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			need(s, p.Conversations, "conversations")
			conv, err := dbpkg.GetPluginConversationBySession(h.deps.DB, id, s.Str(c, 0, "sessionId"))
			if errors.Is(err, sql.ErrNoRows) || (err == nil && conv.ProjectID != project(s)) {
				return goja.Null()
			}
			if err != nil {
				s.Throw("the conversation could not be read")
			}
			return s.JS(convPlain(conv))
		},
		"list": func(s *plugins.Scope, _ goja.FunctionCall) goja.Value {
			need(s, p.Conversations, "conversations")
			list, err := dbpkg.ListPluginConversations(h.deps.DB, id, project(s), "")
			if err != nil {
				s.Throw("the conversations could not be read")
			}
			out := make([]any, 0, len(list))
			for _, conv := range list {
				out = append(out, convPlain(conv))
			}
			return s.JS(out)
		},
		"save": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			need(s, p.Conversations, "conversations")
			o := s.Obj(c, 0)
			sessionID, _ := o["sessionId"].(string)
			if sessionID == "" {
				s.Throw("sessionId is required")
			}
			checkSession(s, sessionID)
			conv := dbpkg.PluginConversation{PluginID: id, SessionID: sessionID, ProjectID: project(s)}
			conv.URL, _ = o["url"].(string)
			conv.Title, _ = o["title"].(string)
			conv.Source, _ = o["source"].(string)
			if n, ok := o["turns"].(float64); ok {
				conv.Turns = int(n)
			}
			if o["meta"] != nil {
				conv.Meta, _ = json.Marshal(o["meta"])
			}
			if conv.URL != "" {
				if u, err := url.Parse(conv.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
					s.Throw("a conversation's url must be http or https")
				}
			}
			saved, err := dbpkg.SavePluginConversation(h.deps.DB, conv)
			if err != nil {
				s.Throw("the conversation could not be saved")
			}
			return s.JS(convPlain(saved))
		},
		"bind": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			need(s, p.Conversations, "conversations")
			cid, sessionID := s.Str(c, 0, "id"), s.Str(c, 1, "sessionId")
			checkSession(s, sessionID)
			conv, err := dbpkg.GetPluginConversation(h.deps.DB, id, cid)
			if err != nil || conv.ProjectID != project(s) {
				s.Throw("no conversation %q", cid)
			}
			if err := dbpkg.BindPluginConversation(h.deps.DB, id, cid, sessionID); err != nil {
				s.Throw("the conversation could not be bound")
			}
			return goja.Undefined()
		},
	}}
}

// ---- sessions ----

func (h *Host) sessionsModule(p plugins.Permissions) plugins.Module {
	get := func(s *plugins.Scope, id string) dbpkg.Session {
		need(s, p.Sessions != "", "sessions")
		sess, err := dbpkg.GetSession(h.deps.DB, id)
		if err != nil || sess.ProjectID != project(s) {
			s.Throw("no session %q in this project", id)
		}
		return sess
	}
	return plugins.Module{Name: "sessions", Funcs: map[string]plugins.Func{
		"get": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			sess := get(s, s.Str(c, 0, "id"))
			return s.JS(map[string]any{
				"id": sess.ID, "name": sess.Name, "model": sess.Model, "kind": sess.Kind, "status": sess.Status,
				"cardId": sess.CardID, "messageCount": sess.MessageCount,
			})
		},
		"messages": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			sess := get(s, s.Str(c, 0, "id"))
			limit := 100
			if n, ok := s.Obj(c, 1)["limit"].(float64); ok && n > 0 && n <= 1000 {
				limit = int(n)
			}
			msgs, err := dbpkg.ListMessages(h.deps.DB, sess.ID)
			if err != nil {
				s.Throw("the messages could not be read")
			}
			if len(msgs) > limit {
				msgs = msgs[len(msgs)-limit:]
			}
			out := make([]any, 0, len(msgs))
			for _, m := range msgs {
				out = append(out, map[string]any{"role": m.Role, "content": m.Content, "seq": float64(m.Seq), "createdAt": m.CreatedAt.Format(time.RFC3339)})
			}
			return s.JS(out)
		},
	}}
}

// ---- board ----

func (h *Host) boardModule(l *loadedPlugin, p plugins.Permissions) plugins.Module {
	cardOf := func(s *plugins.Scope, id string) dbpkg.KanbanCard {
		card, err := dbpkg.GetKanbanCard(h.deps.DB, id)
		if err != nil || card.ProjectID != project(s) {
			s.Throw("no card %q in this project", id)
		}
		return card
	}
	cause := func(s *plugins.Scope) plugins.Cause {
		return plugins.Cause{Source: "plugin:" + l.row.ID, FlowID: s.Inv.Context.FlowID, RunID: s.Inv.Context.RunID, Depth: s.Inv.Context.Depth + 1}
	}
	write := func(s *plugins.Scope) { need(s, p.Board == "write", "board:write") }
	read := func(s *plugins.Scope) { need(s, p.Board != "", "board:read") }
	return plugins.Module{Name: "board", Funcs: map[string]plugins.Func{
		"listLists": func(s *plugins.Scope, _ goja.FunctionCall) goja.Value {
			read(s)
			lists, err := dbpkg.ListKanbanLists(h.deps.DB, project(s))
			if err != nil {
				s.Throw("the board could not be read")
			}
			return s.JS(lists)
		},
		"listCards": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			read(s)
			cards, err := dbpkg.ListKanbanCards(h.deps.DB, project(s), false)
			if err != nil {
				s.Throw("the board could not be read")
			}
			listID, _ := s.Obj(c, 0)["listId"].(string)
			out := []dbpkg.KanbanCard{}
			for _, card := range cards {
				if listID == "" || card.ListID == listID {
					out = append(out, card)
				}
			}
			return s.JS(out)
		},
		"getCard": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			read(s)
			return s.JS(cardOf(s, s.Str(c, 0, "id")))
		},
		"createCard": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			write(s)
			o := s.Obj(c, 0)
			title, _ := o["title"].(string)
			description, _ := o["description"].(string)
			status, _ := o["status"].(string)
			agent, _ := o["agent"].(string)
			if strings.TrimSpace(title) == "" {
				s.Throw("a card needs a title")
			}
			id, err := h.deps.Services.CreateCard(project(s), title, description, status, agent, cause(s))
			if err != nil {
				s.Throw("%s", err.Error())
			}
			return s.JS(map[string]any{"id": id})
		},
		"moveCard": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			write(s)
			card := cardOf(s, s.Str(c, 0, "id"))
			o := s.Obj(c, 1)
			kind, _ := o["toKind"].(string)
			list, _ := o["toListId"].(string)
			if kind == "" && list == "" {
				s.Throw("moveCard needs toKind or toListId")
			}
			rejected, err := h.deps.Services.MoveCard(card.ID, kind, list, cause(s))
			if err != nil {
				s.Throw("%s", err.Error())
			}
			return s.JS(map[string]any{"moved": rejected == "", "rejected": rejected})
		},
		"comment": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			write(s)
			card := cardOf(s, s.Str(c, 0, "id"))
			if err := h.deps.Services.CommentCard(card.ID, l.manifest.Name, s.Str(c, 1, "text")); err != nil {
				s.Throw("%s", err.Error())
			}
			return goja.Undefined()
		},
		"addTag": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			write(s)
			card := cardOf(s, s.Str(c, 0, "id"))
			if err := h.deps.Services.AddCardTag(card.ID, s.Str(c, 1, "tag")); err != nil {
				s.Throw("%s", err.Error())
			}
			return goja.Undefined()
		},
	}}
}

// ---- flows ----

func (h *Host) flowsModule(l *loadedPlugin, p plugins.Permissions) plugins.Module {
	emitted := 0
	return plugins.Module{Name: "flows", Funcs: map[string]plugins.Func{
		"emit": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			need(s, p.Flows, "flows")
			if emitted++; emitted > maxEmits {
				s.Throw("one call can fire at most %d flow events", maxEmits)
			}
			name := s.Str(c, 0, "name")
			payload := s.Plain(c.Argument(1))
			if raw, _ := json.Marshal(payload); len(raw) > maxEmitBytes {
				s.Throw("a flow event's payload is limited to %d KB", maxEmitBytes>>10)
			}
			ctx := s.Inv.Context
			cause := dbpkg.EventCause{Source: "plugin:" + l.row.ID, FlowID: ctx.FlowID, RunID: ctx.RunID, Depth: ctx.Depth + 1}
			if err := dbpkg.RecordPluginEvent(h.deps.DB, project(s), ctx.CardID, ctx.SessionID, map[string]any{
				"plugin": l.row.ID, "event": name, "payload": payload,
			}, cause); err != nil {
				s.Throw("the flow event could not be recorded")
			}
			h.deps.Services.NotifyFlows()
			return goja.Undefined()
		},
	}}
}

// ---- browser ----

func originOf(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", errors.New("not an address")
	}
	return strings.ToLower(u.Scheme + "://" + u.Host), nil
}

func originAllowed(raw string, origins []string) bool {
	o, err := originOf(raw)
	if err != nil {
		return false
	}
	for _, allowed := range origins {
		if strings.EqualFold(o, allowed) {
			return true
		}
	}
	return false
}

// pageOrigin names where a page is, and no more: the rest of its address may be private.
func pageOrigin(raw string) string {
	if o, err := originOf(raw); err == nil {
		return o
	}
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" {
		return u.Scheme + ":"
	}
	return "an unknown page"
}

func (h *Host) driver(l *loadedPlugin) plugins.BrowserDriver {
	l.browserMu.Lock()
	defer l.browserMu.Unlock()
	if l.browser == nil {
		if h.deps.Browser != nil {
			l.browser = h.deps.Browser(l.row.ID)
		} else {
			l.browser = newEdgeDriver(h.deps.DataDir, l.row.ID, l.row.Source)
		}
	}
	return l.browser
}

func (h *Host) closeBrowser(l *loadedPlugin) {
	if l == nil {
		return
	}
	l.browserMu.Lock()
	b := l.browser
	l.browser = nil
	l.browserMu.Unlock()
	if b != nil {
		go b.Close() // it can take seconds
	}
}

func (h *Host) browserModule(l *loadedPlugin, p plugins.Permissions) plugins.Module {
	allowed := func(s *plugins.Scope) []string {
		if p.Browser == nil || len(p.Browser.Origins) == 0 {
			s.Throw("this needs the browser permission, which this plugin was not granted")
		}
		return p.Browser.Origins
	}
	// call runs a driver method away from the VM, so a stuck browser cannot outlive the deadline
	call := func(s *plugins.Scope, work func(b plugins.BrowserDriver) (any, error)) goja.Value {
		allowed(s)
		b := h.driver(l)
		return s.JS(plugins.Wait(s, func(context.Context) (any, error) { return work(b) }))
	}
	// what a page shows is only for pages on an allowed origin: a page can leave for anywhere by
	// itself, and from there it must have nothing to tell. Checked again right before each operation.
	onPage := func(s *plugins.Scope, work func(b plugins.BrowserDriver) (any, error)) goja.Value {
		origins := allowed(s)
		return call(s, func(b plugins.BrowserDriver) (any, error) {
			cur, err := b.URL()
			if err != nil {
				return nil, err
			}
			if !originAllowed(cur, origins) {
				return nil, fmt.Errorf("the page is on %q, not an origin this plugin may use", pageOrigin(cur))
			}
			return work(b)
		})
	}
	checkURL := func(s *plugins.Scope, raw string) {
		origins := allowed(s)
		if raw == "about:blank" {
			return // an empty page to start from; evalPage stays refused there, it is no origin
		}
		if !originAllowed(raw, origins) {
			s.Throw("%q is not on an origin this plugin may visit", raw)
		}
	}
	return plugins.Module{Name: "browser", Funcs: map[string]plugins.Func{
		"launch": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			allowed(s)
			headless, _ := s.Obj(c, 0)["headless"].(bool)
			return call(s, func(b plugins.BrowserDriver) (any, error) { return b.Launch(headless) })
		},
		"close": func(s *plugins.Scope, _ goja.FunctionCall) goja.Value {
			return call(s, func(b plugins.BrowserDriver) (any, error) { return b.Close(), nil })
		},
		"status": func(s *plugins.Scope, _ goja.FunctionCall) goja.Value {
			return call(s, func(b plugins.BrowserDriver) (any, error) { return b.Status(), nil })
		},
		"tabs": func(s *plugins.Scope, _ goja.FunctionCall) goja.Value {
			return call(s, func(b plugins.BrowserDriver) (any, error) { return b.Tabs() })
		},
		"info": func(s *plugins.Scope, _ goja.FunctionCall) goja.Value {
			return call(s, func(b plugins.BrowserDriver) (any, error) { return b.Info() })
		},
		"newTab": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			raw := s.Str(c, 0, "url")
			checkURL(s, raw)
			return call(s, func(b plugins.BrowserDriver) (any, error) { return b.NewTab(raw) })
		},
		"switchTab": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			id := s.Str(c, 0, "id")
			return call(s, func(b plugins.BrowserDriver) (any, error) { return b.SwitchTab(id) })
		},
		"closeTab": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			id := s.Str(c, 0, "id")
			return call(s, func(b plugins.BrowserDriver) (any, error) { return nil, b.CloseTab(id) })
		},
		"navigate": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			raw := s.Str(c, 0, "url")
			checkURL(s, raw)
			return call(s, func(b plugins.BrowserDriver) (any, error) { return b.Navigate(raw) })
		},
		"find": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			o := s.Obj(c, 0)
			role, _ := o["role"].(string)
			name, _ := o["name"].(string)
			selector, _ := o["selector"].(string)
			exact, _ := o["exact"].(bool)
			limit := 0
			if n, ok := o["limit"].(float64); ok {
				limit = int(n)
			}
			return onPage(s, func(b plugins.BrowserDriver) (any, error) { return b.Find(role, name, selector, exact, limit) })
		},
		"click": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			ref := s.Str(c, 0, "ref")
			return onPage(s, func(b plugins.BrowserDriver) (any, error) { return b.Click(ref) })
		},
		"type": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			ref, text := s.Str(c, 0, "ref"), s.Str(c, 1, "text")
			o := s.Obj(c, 2)
			submit, _ := o["submit"].(bool)
			clear, _ := o["clear"].(bool)
			return onPage(s, func(b plugins.BrowserDriver) (any, error) { return b.Type(ref, text, submit, clear) })
		},
		"press": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			combo := s.Str(c, 0, "combo")
			return onPage(s, func(b plugins.BrowserDriver) (any, error) { return b.Press(combo) })
		},
		"evalPage": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			code := s.Str(c, 0, "js")
			return onPage(s, func(b plugins.BrowserDriver) (any, error) { return b.Eval(code) })
		},
		"text": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			ref := ""
			if a := c.Argument(0); !goja.IsUndefined(a) && !goja.IsNull(a) {
				ref = a.String()
			}
			limit := int(c.Argument(1).ToInteger())
			return onPage(s, func(b plugins.BrowserDriver) (any, error) { return b.Text(ref, limit) })
		},
	}}
}

// ---- runners ----

func (h *Host) runnersModule(l *loadedPlugin) plugins.Module {
	return plugins.Module{Name: "runners", Funcs: map[string]plugins.Func{
		"list": func(s *plugins.Scope, _ goja.FunctionCall) goja.Value {
			out := []any{}
			if h.deps.Peers != nil {
				for _, p := range h.deps.Peers() {
					out = append(out, map[string]any{"id": p.ID, "hostname": p.Hostname, "policy": p.Policy, "version": p.Version})
				}
			}
			return s.JS(out)
		},
		"call": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
			if l.manifest.Targets.Runner == nil {
				s.Throw("this plugin has no runner target")
			}
			runnerID := s.Str(c, 0, "runnerId")
			name := s.Str(c, 1, "rpcName")
			arg := s.Plain(c.Argument(2))
			o := s.Obj(c, 3)
			timeout := plugins.RPCSeconds * time.Second
			if ms, ok := o["timeoutMs"].(float64); ok && ms > 0 {
				timeout = time.Duration(ms) * time.Millisecond
			}
			if left := time.Until(deadline(s)); timeout > left {
				timeout = left
			}
			// the plugin says which session or card, and the hub says where that is
			ctx := s.Inv.Context
			sessionID, cardID := ctx.SessionID, ctx.CardID
			if w, ok := o["workspace"].(map[string]any); ok {
				if v, ok := w["sessionId"].(string); ok {
					sessionID = v
				}
				if v, ok := w["cardId"].(string); ok {
					cardID = v
				}
			} else if o["workspace"] != nil {
				s.Throw("workspace names a sessionId or a cardId: a plugin never gives a folder")
			}
			_, dir, err := h.deps.Services.Workspace(ctx.ProjectID, sessionID, cardID, runnerID)
			if err != nil {
				s.Throw("%s", err.Error())
			}
			cc := plugins.Context{ProjectID: ctx.ProjectID, SessionID: sessionID, CardID: cardID, Kind: "rpc", Depth: ctx.Depth}
			res := plugins.Wait(s, func(cx context.Context) (any, error) {
				return h.remote.invoke(cx, l, runnerID, []string{"rpc", name}, arg, timeout, cc, dir)
			})
			return s.JS(res)
		},
	}}
}

func deadline(s *plugins.Scope) time.Time {
	if d, ok := s.Ctx.Deadline(); ok {
		return d
	}
	return time.Now().Add(plugins.RPCSeconds * time.Second)
}
