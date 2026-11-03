package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// The agents table used to have a foreign key on project_id. Opening a database
// created by that version must rebuild it, keep the data and allow global agents.
func TestAgentsTableMigratesAndKeepsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`
CREATE TABLE projects (id TEXT PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE agents (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    model       TEXT NOT NULL DEFAULT 'sonnet',
    account_id  TEXT NOT NULL DEFAULT '',
    personality TEXT NOT NULL DEFAULT '',
    tools       TEXT NOT NULL DEFAULT '["Bash","Edit","Read","Write"]',
    created_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
INSERT INTO projects (id, name) VALUES ('p1', 'one');
INSERT INTO agents (id, project_id, name, model, personality, tools) VALUES ('a1', 'p1', 'Engineer', 'opus', 'writes code', '["Read","Bash"]');
`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	db, err := Init(path)
	if err != nil {
		t.Fatalf("opening an old database must migrate it: %v", err)
	}
	defer db.Close()

	a, err := GetAgent(db, "a1")
	if err != nil {
		t.Fatalf("the existing agent must survive: %v", err)
	}
	if a.Name != "Engineer" || a.Model != "opus" || a.ProjectID != "p1" || a.Scope != "project" || len(a.Tools) != 2 {
		t.Errorf("agent data changed in the migration: %+v", a)
	}
	if len(a.LucodeTools) != len(DefaultLucodeTools) {
		t.Errorf("an existing agent gets every lucode tool, got %v", a.LucodeTools)
	}

	var ddl string
	db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='agents'`).Scan(&ddl)
	if strings.Contains(ddl, "REFERENCES projects") {
		t.Error("the foreign key on project_id should be gone")
	}

	// the point of the change: an agent without a project
	if err := InsertAgent(db, Agent{ID: "g1", ProjectID: "", Name: "Reviewer", Model: "haiku", Executor: "claude"}); err != nil {
		t.Fatalf("a global agent must be insertable: %v", err)
	}

	// opening again is a no-op
	db.Close()
	db2, err := Init(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer db2.Close()
	if _, err := GetAgent(db2, "g1"); err != nil {
		t.Errorf("data must survive a second open: %v", err)
	}
}

func TestGlobalAgentsAreVisibleInEveryProjectAndSurviveProjectDeletion(t *testing.T) {
	db, err := Init(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, id := range []string{"p1", "p2"} {
		if err := InsertProject(db, Project{ID: id, Name: id, WorkDirs: []string{"."}, DefaultModel: "haiku"}); err != nil {
			t.Fatal(err)
		}
	}
	InsertAgent(db, Agent{ID: "local1", ProjectID: "p1", Name: "Local1"})
	InsertAgent(db, Agent{ID: "local2", ProjectID: "p2", Name: "Local2"})
	InsertAgent(db, Agent{ID: "shared", ProjectID: "", Name: "Shared"})

	names := func(project string) string {
		as, _ := ListAgents(db, project)
		var out []string
		for _, a := range as {
			out = append(out, a.Name)
		}
		return strings.Join(out, ",")
	}
	if got := names("p1"); got != "Local1,Shared" {
		t.Errorf("p1 should see its own agent and the global one, got %q", got)
	}
	if got := names("p2"); got != "Local2,Shared" {
		t.Errorf("p2 should see its own agent and the global one, got %q", got)
	}

	if err := DeleteProject(db, "p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := GetAgent(db, "local1"); err == nil {
		t.Error("deleting a project deletes the agents that belong to it")
	}
	if _, err := GetAgent(db, "shared"); err != nil {
		t.Error("a global agent must survive the deletion of any project")
	}
	if got := names("p2"); got != "Local2,Shared" {
		t.Errorf("p2 is unaffected, got %q", got)
	}
}

func TestLucodeToolsDefaultAllAndEmptyMeansNone(t *testing.T) {
	db, err := Init(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	InsertAgent(db, Agent{ID: "all", Name: "All"})                            // nil -> every tool
	InsertAgent(db, Agent{ID: "none", Name: "None", LucodeTools: []string{}}) // explicitly none
	InsertAgent(db, Agent{ID: "some", Name: "Some", LucodeTools: []string{"task_done"}})

	all, _ := GetAgent(db, "all")
	none, _ := GetAgent(db, "none")
	some, _ := GetAgent(db, "some")
	if len(all.LucodeTools) != len(DefaultLucodeTools) {
		t.Errorf("an agent created without a list gets every lucode tool, got %v", all.LucodeTools)
	}
	if none.LucodeTools == nil || len(none.LucodeTools) != 0 {
		t.Errorf("an empty list must stay empty (no lucode tools), got %v", none.LucodeTools)
	}
	if len(some.LucodeTools) != 1 || some.LucodeTools[0] != "task_done" {
		t.Errorf("a chosen subset must round-trip, got %v", some.LucodeTools)
	}
}
