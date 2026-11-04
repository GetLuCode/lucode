package db

import (
	"path/filepath"
	"testing"
)

func TestSessionWorkspaceSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	d, err := Init(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := InsertProject(d, Project{ID: "p", Name: "p", WorkDirs: []string{"."}, DefaultModel: "haiku"}); err != nil {
		t.Fatal(err)
	}
	if err := InsertSession(d, Session{ID: "s", ProjectID: "p", Name: "s", Model: "haiku", Status: "idle"}); err != nil {
		t.Fatal(err)
	}
	if GetSessionWorkspace(d, "s") != nil {
		t.Fatal("workspace before any was saved")
	}
	ws := map[string]any{"type": "claude_workspace", "branch": "lucode-abc", "path": "/tmp/wt", "worktree": true}
	if err := SaveSessionWorkspace(d, "s", ws); err != nil {
		t.Fatal(err)
	}
	ws["branch"] = "lucode-def" // a later turn replaces it
	if err := SaveSessionWorkspace(d, "s", ws); err != nil {
		t.Fatal(err)
	}
	d.Close()

	d, err = Init(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	got := GetSessionWorkspace(d, "s")
	if got["branch"] != "lucode-def" || got["worktree"] != true || got["path"] != "/tmp/wt" {
		t.Errorf("after reopen: %v", got)
	}
}
