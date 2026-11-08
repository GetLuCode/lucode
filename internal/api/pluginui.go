package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/plugins/surface"
)

// A plugin's frontend page runs in a Worker of the browser, never in the page itself (see
// internal/plugins/surface). The page asks for the page's ticket with its token, then starts the
// Worker on the script's address, which carries the ticket instead of the token.

const pluginUIScriptPrefix = "/api/plugin-ui/"

// isPluginUIScript is the one request the auth lets through for a ticket: a GET of exactly one
// clean segment, for this instance. A request meant for another instance must carry the token,
// because that instance trusts what this one forwards.
func isPluginUIScript(r *http.Request) bool {
	rest, ok := strings.CutPrefix(r.URL.Path, pluginUIScriptPrefix)
	return ok && r.Method == http.MethodGet && rest != "" && !strings.ContainsAny(rest, `/\%`) &&
		!strings.Contains(rest, "..") && r.Header.Get("X-Lucode-Remote") == ""
}

func (h *Handler) registerPluginUI(mux *http.ServeMux, need func(http.HandlerFunc) http.HandlerFunc) {
	if h.uiTickets == nil {
		h.uiTickets = surface.NewTickets()
	}
	mux.HandleFunc("POST /api/plugins/{id}/pages/{page}/ui", need(h.pluginPageUI))
	mux.HandleFunc("GET "+pluginUIScriptPrefix+"{ticket}", need(h.pluginPageScript))
}

type pluginPageUIResponse struct {
	Script string   `json:"script"` // where the Worker loads from: it works once, for a minute
	Style  string   `json:"style"`  // the page's stylesheet, which the browser checks before it uses it
	RPC    []string `json:"rpc"`    // what the page may call; the hub checks it again on each call
}

func (h *Handler) pluginPageUI(w http.ResponseWriter, r *http.Request) {
	if h.uiTickets == nil {
		http.Error(w, "plugin pages are not running on this instance", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body struct {
		ProjectID string `json:"projectId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.ProjectID != "" {
		if _, err := dbpkg.GetProject(h.db, body.ProjectID); err != nil {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
	}
	id, page := r.PathValue("id"), r.PathValue("page")
	def, _, style, _, err := h.plugins.PageUI(id, page)
	if err != nil {
		pluginError(w, err)
		return
	}
	rpcs := def.RPC
	if rpcs == nil {
		rpcs = []string{}
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonResp(w, http.StatusOK, pluginPageUIResponse{
		Script: pluginUIScriptPrefix + h.uiTickets.Mint(id, page, body.ProjectID),
		Style:  string(style),
		RPC:    rpcs,
	})
}

// pluginPageScript is the Worker's script. Its policy keeps the Worker off the network, and it is
// never a document of this origin: a script type, no sniffing, no framing.
func (h *Handler) pluginPageScript(w http.ResponseWriter, r *http.Request) {
	if h.uiTickets == nil {
		http.Error(w, "plugin pages are not running on this instance", http.StatusServiceUnavailable)
		return
	}
	c, err := h.uiTickets.Redeem(r.PathValue("ticket"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	_, script, _, m, err := h.plugins.PageUI(c.PluginID, c.PageID)
	if err != nil {
		pluginError(w, err)
		return
	}
	out := surface.Script(surface.Context{
		PluginID: m.ID, PluginName: m.Name, PluginVersion: m.Version, PageID: c.PageID, ProjectID: c.ProjectID,
	}, script)
	hdr := w.Header()
	hdr.Set("Content-Type", "text/javascript; charset=utf-8")
	hdr.Set("Content-Security-Policy", surface.CSP)
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Cross-Origin-Resource-Policy", "same-origin")
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write(out)
}
