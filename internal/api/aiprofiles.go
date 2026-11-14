package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	dbpkg "claude-orchestrator/internal/db"
	versionpkg "claude-orchestrator/internal/version"

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
	dir := expandConfigDir(body.ClaudeConfigDir)
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

var winEnvVar = regexp.MustCompile(`%([^%\\/]+)%`)

// expandConfigDir turns what the user typed into the path on this runner: a leading
// "~", %VAR% (Windows style) and $VAR / ${VAR} are replaced with the runner's own
// environment, so "%USERPROFILE%\.claude-work" works. The stored path is the
// expanded one because claude receives it as CLAUDE_CONFIG_DIR, which nobody expands.
// An unknown variable is left as typed so the error shows what was meant.
func expandConfigDir(in string) string {
	dir := strings.TrimSpace(in)
	if dir == "" {
		return ""
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			dir = home + dir[1:]
		}
	}
	dir = winEnvVar.ReplaceAllStringFunc(dir, func(m string) string {
		if v := os.Getenv(m[1 : len(m)-1]); v != "" {
			return v
		}
		return m
	})
	dir = os.Expand(dir, func(name string) string {
		if v := os.Getenv(name); v != "" {
			return v
		}
		return "$" + name
	})
	return filepath.Clean(dir)
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

const anthropicUsageURL = "https://api.anthropic.com/api/oauth/usage"

var (
	usageMu    sync.Mutex
	usageStore = map[string]usageCacheEntry{}
)

type usageCacheEntry struct {
	body    []byte
	expires time.Time
}

// aiProfileUsage returns the plan usage windows (5h, 7d...) of the account behind
// the profile, as Anthropic's /api/oauth/usage reports them. Cached for a minute
// so many open tabs do not hammer Anthropic.
func (h *Handler) aiProfileUsage(w http.ResponseWriter, r *http.Request) {
	p, err := dbpkg.GetAIProfile(h.db, r.PathValue("id"))
	if err != nil {
		http.Error(w, "ai profile not found", http.StatusNotFound)
		return
	}
	usageMu.Lock()
	if e, ok := usageStore[p.ID]; ok && time.Now().Before(e.expires) {
		usageMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write(e.body)
		return
	}
	usageMu.Unlock()
	_, token, _, expiresAt, err := dbpkg.ReadCredentials(p.ClaudeConfigDir)
	if err != nil || token == "" {
		http.Error(w, "no credentials in this dir", http.StatusUnprocessableEntity)
		return
	}
	if expiresAt > 0 && expiresAt < time.Now().UnixMilli() {
		http.Error(w, "the access token expired: use claude with this dir once to renew it", http.StatusUnprocessableEntity)
		return
	}
	req, _ := http.NewRequest(http.MethodGet, anthropicUsageURL, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "claude-code/2.x")
	client := http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "could not reach Anthropic: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("Anthropic answered %d", resp.StatusCode), http.StatusBadGateway)
		return
	}
	usageMu.Lock()
	usageStore[p.ID] = usageCacheEntry{body: body, expires: time.Now().Add(time.Minute)}
	usageMu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

// ---- hub side: the same actions on any runner ----

type runnerInfo struct {
	ID         string `json:"id"`
	Hostname   string `json:"hostname"`
	OS         string `json:"os"`
	Self       bool   `json:"self"`
	Version    string `json:"version,omitempty"`    // short git commit it was built from
	StartedAt  string `json:"started_at,omitempty"` // RFC3339
	RunnerOnly bool   `json:"runner_only"`          // lucode-runner: no web, no API of a hub
}

// listRunners returns this instance first, then the lucode peers.
func (h *Handler) listRunners(w http.ResponseWriter, r *http.Request) {
	self := h.bus.SelfInfo()
	out := []runnerInfo{}
	if !h.hubOnly { // a hub runs nothing itself: it is not a place to send a project to
		v := versionpkg.Current("hub")
		out = append(out, runnerInfo{ID: self.ID, Hostname: self.Hostname, OS: self.OS, Self: true, Version: v.Short, StartedAt: v.StartedAt.Format(time.RFC3339)})
	}
	for _, p := range h.bus.Peers() {
		if p.Type == "lucode" && p.ID != self.ID && p.IsRunner() {
			ver, _ := p.Extra["version"].(string)
			only, _ := p.Extra["runner_only"].(bool)
			started := ""
			if !p.StartedAt.IsZero() {
				started = p.StartedAt.Format(time.RFC3339)
			}
			out = append(out, runnerInfo{ID: p.ID, Hostname: p.Hostname, OS: p.OS, Version: ver, StartedAt: started, RunnerOnly: only})
		}
	}
	jsonResp(w, http.StatusOK, out)
}

// runnerAI runs one of the actions on the runner named in the path: locally
// when it is this instance, otherwise on the peer over NATS.
func (h *Handler) runnerAI(local http.HandlerFunc, target func(r *http.Request) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rid := r.PathValue("rid")
		if rid == "local" || rid == h.bus.SelfInfo().ID {
			local(w, r)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = target(r)
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

// isRemoteRunner reports whether id names a peer, not this instance.
func (h *Handler) isRemoteRunner(id string) bool {
	return id != "" && id != "local" && id != h.bus.SelfInfo().ID
}

// projectWorkDirs are the directories a project uses on the runner it works on.
func (h *Handler) projectWorkDirs(p dbpkg.Project) []string {
	if h.isRemoteRunner(p.RunnerID) {
		return p.EnvDirs[p.RunnerID]
	}
	return p.WorkDirs
}

// turnEnv is where and with which AI an agent's turn runs: the agent's own choice
// wins over the project's. An AI belongs to its runner, so when the agent picks a
// different runner than the project's, the project's AI does not carry over.
type turnEnv struct {
	RunnerID    string   // "" = this instance
	AIProfileID string   // preconfigured AI of that runner ("" = default account)
	WorkDirs    []string // directories on that runner
	ConfigDir   string   // claude_config_dir, resolved here only for this instance
}

// runnerLabel names a runner for an error message: its host name when it is on the network.
func (h *Handler) runnerLabel(id string) string {
	for _, p := range h.bus.Peers() {
		if p.ID == id && p.Hostname != "" {
			return fmt.Sprintf("%s (%.8s)", p.Hostname, id)
		}
	}
	return fmt.Sprintf("runner %.8s (offline)", id)
}

func (h *Handler) resolveTurnEnv(agent dbpkg.Agent, p dbpkg.Project) (turnEnv, error) {
	norm := func(id string) string {
		if h.isRemoteRunner(id) {
			return id
		}
		return ""
	}
	runner, aiID := norm(p.RunnerID), p.AIProfileID
	if agent.RunnerID != "" {
		if want := norm(agent.RunnerID); want != runner {
			runner, aiID = want, "" // another runner: the project's AI is not valid there
		}
	}
	if agent.AIProfileID != "" {
		aiID = agent.AIProfileID
	} else if act := loadActiveAI(); act.AIProfileID != "" && norm(act.RunnerID) == runner {
		aiID = act.AIProfileID // "Project's AI" / default: the account picked in the usage container
	}
	env := turnEnv{RunnerID: runner, AIProfileID: aiID}
	if runner == "" && h.hubOnly {
		return env, fmt.Errorf("this instance only orchestrates (--hub-only) and runs no claude: choose a runner for project %q in its settings", p.Name)
	}
	if runner != "" {
		env.WorkDirs = p.EnvDirs[runner]
		if len(env.WorkDirs) == 0 {
			var has []string
			for id, dirs := range p.EnvDirs {
				if len(dirs) > 0 {
					has = append(has, h.runnerLabel(id))
				}
			}
			sort.Strings(has)
			where := "it has no work dir on any runner"
			if len(has) > 0 {
				where = "it only has a work dir on " + strings.Join(has, ", ")
			}
			return env, fmt.Errorf("the chat is set to run on %s, but project %q has no work dir there (%s). Pick that runner in the chat, or add a work dir for %s in the project's Environment (⋯ menu)", h.runnerLabel(runner), p.Name, where, h.runnerLabel(runner))
		}
		return env, nil // the runner resolves the AI's config dir itself
	}
	env.WorkDirs = p.WorkDirs
	env.ConfigDir = p.ClaudeConfigDir
	if aiID != "" {
		if prof, err := dbpkg.GetAIProfile(h.db, aiID); err == nil {
			env.ConfigDir = prof.ClaudeConfigDir
		}
	}
	return env, nil
}
