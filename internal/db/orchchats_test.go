package db

import (
	"path/filepath"
	"testing"
)

// An orchestrator conversation can be renamed and assigned to another project, and the
// history finds it there afterwards.
func TestMoveOrchestratorChat(t *testing.T) {
	db, err := Init(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, id := range []string{"p1", "p2"} {
		if _, err := db.Exec(`INSERT INTO projects (id, name) VALUES (?,?)`, id, "proj "+id); err != nil {
			t.Fatal(err)
		}
	}
	if err := InsertSession(db, Session{ID: "s1", ProjectID: "p1", Name: "Orchestrator", Model: "sonnet", Status: "idle", Kind: SessionKindOrchestrator}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sessions SET message_count=2 WHERE id='s1'`); err != nil {
		t.Fatal(err)
	}
	if err := InsertMessage(db, Message{ID: "m1", SessionID: "s1", Role: "user", Content: "hello", Seq: 1}); err != nil {
		t.Fatal(err)
	}

	if err := UpdateSessionName(db, "s1", "My chat"); err != nil {
		t.Fatal(err)
	}
	if err := MoveOrchestratorChat(db, "s1", "p2"); err != nil {
		t.Fatal(err)
	}
	chats, err := ListOrchestratorChats(db, "p2")
	if err != nil || len(chats) != 1 {
		t.Fatalf("p2 chats = %v, %v", chats, err)
	}
	if c := chats[0]; c.Name != "My chat" || c.ProjectName != "proj p2" || c.FirstUser != "hello" {
		t.Fatalf("chat = %+v", c)
	}
	if old, _ := ListOrchestratorChats(db, "p1"); len(old) != 0 {
		t.Fatalf("p1 still lists it: %v", old)
	}
	if all, _ := ListOrchestratorChats(db, ""); len(all) != 1 {
		t.Fatalf("all = %v", all)
	}
}
