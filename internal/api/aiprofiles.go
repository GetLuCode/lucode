package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	dbpkg "claude-orchestrator/internal/db"

	"github.com/google/uuid"
)

// Runners and their preconfigured AIs.
//
// A runner is a lucode instance (this one or a peer found over NATS). Each runner
// owns a list of preconfigured AIs: a claude_config_dir plus the account the
// Anthropic profile endpoint reports for its credentials (what "/status" shows).
// The list is read and changed through the runner's own /api/ai-profiles
// endpoints; the hub reaches a peer's through /api/runners/{rid}/ai-profiles,
// which forwards over NATS.

const anthropicProfileURL = "https://api.anthropic.com/api/oauth/profile"

// ---- runner side: the endpoints every lucode serves for its own AIs ----

func (h *Handler) listAIProfiles(w http.ResponseWriter, r *http.Request) {
	list, err := dbpkg.ListAIProfiles(h.db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusOK, list)
}

func (h *Handler) createAIProfile(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name            string `json:"name"`
		ClaudeConfigDir string `json:"claude_config_dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	dir := strings.TrimSpace(body.ClaudeConfigDir)
	if dir == "" {
		http.Error(w, "claude_config_dir is required", http.StatusBadRequest)
		return
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		http.Error(w, "claude_config_dir does not exist on this runner: "+dir, http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = filepath.Base(dir)
	}
	p := dbpkg.AIProfile{ID: uuid.New().String(), Name: name, ClaudeConfigDir: dir}
	fillAIProfileAccount(&p)
	if err := dbpkg.InsertAIProfile(h.db, p); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			http.Error(w, "that claude_config_dir is already configured", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusCreated, p)
}

func (h *Handler) refreshAIProfile(w http.ResponseWriter, r *http.Request) {
	p, err := dbpkg.GetAIProfile(h.db, r.PathValue("id"))
	if err != nil {
		http.Error(w, "ai profile not found", http.StatusNotFound)
		return
	}
	fillAIProfileAccount(&p)
	if err := dbpkg.UpdateAIProfileAccount(h.db, p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusOK, p)
}

func (h *Handler) deleteAIProfile(w http.ResponseWriter, r *http.Request) {
	if err := dbpkg.DeleteAIProfile(h.db, r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fillAIProfileAccount asks Anthropic who owns the credentials in the profile's
// dir. A failure is recorded in p.Error instead of aborting: the AI can still be
// saved and refreshed later (for example after logging in again).
func fillAIProfileAccount(p *dbpkg.AIProfile) {
	p.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	p.Error = ""
	sub, token, _, expiresAt, err := dbpkg.ReadCredentials(p.ClaudeConfigDir)
	if err != nil || token == "" {
		p.Error = "no credentials in this dir: log in with claude using CLAUDE_CONFIG_DIR set to it"
		return
	}
	if expiresAt > 0 && expiresAt < time.Now().UnixMilli() {
		p.Error = "the access token expired: use claude with this dir once to renew it, then refresh"
		return
	}
	req, _ := http.NewRequest(http.MethodGet, anthropicProfileURL, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "claude-code/2.x")
	client := http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		p.Error = "could not reach Anthropic: " + err.Error()
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		p.Error = fmt.Sprintf("Anthropic answered %d", resp.StatusCode)
		return
	}
	p.Email, p.DisplayName, p.Organization, p.Plan = parseAnthropicProfile(body)
	if p.Plan == "" {
		p.Plan = sub // the subscription type recorded in the credentials file
	}
}

// parseAnthropicProfile pulls the fields shown for an AI out of the profile reply.
func parseAnthropicProfile(body []byte) (email, display, org, plan string) {
	var v struct {
		Account struct {
			Email       string `json:"email"`
			FullName    string `json:"full_name"`
			DisplayName string `json:"display_name"`
		} `json:"account"`
		Organization struct {
			Name             string `json:"name"`
			OrganizationType string `json:"organization_type"`
			RateLimitTier    string `json:"rate_limit_tier"`
		} `json:"organization"`
	}
	_ = json.Unmarshal(body, &v)
	display = v.Account.DisplayName
	if display == "" {
		display = v.Account.FullName
	}
	plan = v.Organization.OrganizationType
	if plan == "" {
		plan = v.Organization.RateLimitTier
	}
	return v.Account.Email, display, v.Organization.Name, plan
}

// ---- hub side: the same actions on any runner ----

type runnerInfo struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Self     bool   `json:"self"`
}

// listRunners returns this instance first, then the lucode peers.
func (h *Handler) listRunners(w http.ResponseWriter, r *http.Request) {
	self := h.bus.SelfInfo()
	out := []runnerInfo{{ID: self.ID, Hostname: self.Hostname, OS: self.OS, Self: true}}
	for _, p := range h.bus.Peers() {
		if p.Type == "lucode" && p.ID != self.ID {
			out = append(out, runnerInfo{ID: p.ID, Hostname: p.Hostname, OS: p.OS})
		}
	}
	jsonResp(w, http.StatusOK, out)
}

// runnerAI runs one of the AI actions on the runner named in the path: locally
// when it is this instance, otherwise on the peer over NATS.
func (h *Handler) runnerAI(local http.HandlerFunc, suffix func(r *http.Request) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rid := r.PathValue("rid")
		if rid == "local" || rid == h.bus.SelfInfo().ID {
			local(w, r)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/api/ai-profiles" + suffix(r)
		r2.Header.Del("X-Lucode-Remote")
		resp, err := h.bus.RPCRequest(rid, r2)
		if err != nil {
			http.Error(w, "runner unavailable: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}
}

// projectConfigDir is the claude_config_dir a project's turns run with: the one
// of its preconfigured AI when that AI lives on this runner, else the project's own.
func (h *Handler) projectConfigDir(p dbpkg.Project) string {
	if p.AIProfileID != "" && (p.RunnerID == "" || p.RunnerID == h.bus.SelfInfo().ID) {
		if ai, err := dbpkg.GetAIProfile(h.db, p.AIProfileID); err == nil {
			return ai.ClaudeConfigDir
		}
	}
	return p.ClaudeConfigDir
}
