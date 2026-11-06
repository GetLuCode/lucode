package api

import (
	"strings"
	"testing"

	dbpkg "claude-orchestrator/internal/db"
	natspkg "claude-orchestrator/internal/natsbus"
)

func TestResolveTurnEnv(t *testing.T) {
	h, _, _ := newTaskTestHandler(t)
	h.bus = natspkg.NewBus(nil) // only SelfInfo is used
	if err := dbpkg.InsertAIProfile(h.db, dbpkg.AIProfile{ID: "ai-local", Name: "local", ClaudeConfigDir: "/cfg/local"}); err != nil {
		t.Fatal(err)
	}

	local := dbpkg.Project{Name: "p", WorkDirs: []string{"/w"}, ClaudeConfigDir: "/cfg/proj", AIProfileID: "ai-local"}
	remote := dbpkg.Project{Name: "p", WorkDirs: []string{"/w"}, RunnerID: "peer1", AIProfileID: "ai-peer1",
		EnvDirs: map[string][]string{"peer1": {"/peer/w"}}}

	// the project's own environment
	env, err := h.resolveTurnEnv(dbpkg.Agent{}, local)
	if err != nil || env.RunnerID != "" || env.ConfigDir != "/cfg/local" || env.WorkDirs[0] != "/w" {
		t.Errorf("local project: %+v %v", env, err)
	}
	env, err = h.resolveTurnEnv(dbpkg.Agent{}, remote)
	if err != nil || env.RunnerID != "peer1" || env.AIProfileID != "ai-peer1" || env.ConfigDir != "" || env.WorkDirs[0] != "/peer/w" {
		t.Errorf("remote project: %+v %v", env, err)
	}

	// an agent overriding only the AI keeps the project's runner
	env, _ = h.resolveTurnEnv(dbpkg.Agent{AIProfileID: "other"}, remote)
	if env.RunnerID != "peer1" || env.AIProfileID != "other" {
		t.Errorf("agent AI override: %+v", env)
	}

	// an agent on another runner does not inherit the project's AI, and needs a work dir there
	if _, err = h.resolveTurnEnv(dbpkg.Agent{RunnerID: "peer2"}, remote); err == nil || !strings.Contains(err.Error(), "no work dir") {
		t.Errorf("want a missing work dir error, got %v", err)
	}
	remote.EnvDirs["peer2"] = []string{"/p2"}
	env, err = h.resolveTurnEnv(dbpkg.Agent{RunnerID: "peer2"}, remote)
	if err != nil || env.RunnerID != "peer2" || env.AIProfileID != "" || env.WorkDirs[0] != "/p2" {
		t.Errorf("agent on peer2: %+v %v", env, err)
	}

	// an agent pulled back to this instance uses the project's local dirs
	env, err = h.resolveTurnEnv(dbpkg.Agent{RunnerID: "local"}, remote)
	if err != nil || env.RunnerID != "" || env.WorkDirs[0] != "/w" || env.AIProfileID != "" {
		t.Errorf("agent on this instance: %+v %v", env, err)
	}
}

// A hub (--hub-only) runs no claude: a project or agent that would run on this
// instance is an error that says what to do, and the runners it offers do not include
// itself. Projects on a runner are unaffected.
func TestHubOnlyNeverRunsLocally(t *testing.T) {
	h, _, _ := newTaskTestHandler(t)
	h.bus = natspkg.NewBus(nil)
	h.SetHubOnly(true)

	local := dbpkg.Project{Name: "p", WorkDirs: []string{"/w"}}
	remote := dbpkg.Project{Name: "p", RunnerID: "peer1", EnvDirs: map[string][]string{"peer1": {"/peer/w"}}}

	if _, err := h.resolveTurnEnv(dbpkg.Agent{}, local); err == nil || !strings.Contains(err.Error(), "--hub-only") {
		t.Errorf("a project without runner must fail with a hint, got %v", err)
	}
	if _, err := h.resolveTurnEnv(dbpkg.Agent{RunnerID: "local"}, remote); err == nil {
		t.Error("an agent pulled back to the hub must fail")
	}
	env, err := h.resolveTurnEnv(dbpkg.Agent{}, remote)
	if err != nil || env.RunnerID != "peer1" || env.WorkDirs[0] != "/peer/w" {
		t.Errorf("a project on a runner must work: %+v %v", env, err)
	}
}
