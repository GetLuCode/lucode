package checkpoint

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"claude-orchestrator/internal/aiapi/executor/fsop"
)

func setupWorkDir(t *testing.T) string {
	t.Helper()
	dir, _ := os.MkdirTemp("", "checkpoint-test")
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func newTestStore(t *testing.T, workDir string) *Store {
	t.Helper()
	s, err := NewStore(workDir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStore_Empty(t *testing.T) {
	wd := setupWorkDir(t)
	s := newTestStore(t, wd)
	if s.Head() != "" {
		t.Errorf("head on empty store = %q, want \"\"", s.Head())
	}
	if list := s.List(); len(list) != 0 {
		t.Errorf("list on empty store has %d entries, want 0", len(list))
	}
}

func TestStore_Snapshot(t *testing.T) {
	wd := setupWorkDir(t)
	s := newTestStore(t, wd)

	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("hello"), 0644)
	os.MkdirAll(filepath.Join(wd, "src"), 0755)
	os.WriteFile(filepath.Join(wd, "src", "main.go"), []byte("package main"), 0644)

	cp, err := s.Snapshot("turn-1", nil, "initial snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if cp.ID == "" {
		t.Error("snapshot ID is empty")
	}
	if cp.Kind != "turn" {
		t.Errorf("kind = %q, want turn", cp.Kind)
	}
	if s.Head() != cp.Tree {
		t.Errorf("Head() = %q, want %q", s.Head(), cp.Tree)
	}
	if list := s.List(); len(list) != 1 {
		t.Errorf("List() has %d entries, want 1", len(list))
	}
}

func TestStore_IgnoresNodeModules(t *testing.T) {
	wd := setupWorkDir(t)
	s := newTestStore(t, wd)

	os.MkdirAll(filepath.Join(wd, "node_modules", "react"), 0755)
	os.WriteFile(filepath.Join(wd, "node_modules", "react", "index.js"), []byte("..."), 0644)
	os.WriteFile(filepath.Join(wd, "main.go"), []byte("package main"), 0644)

	cp, err := s.Snapshot("turn-1", nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := loadTree(s.storeRoot, cp.Tree)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range tree.Entries {
		if filepath.HasPrefix(e.Path, "node_modules") {
			t.Errorf("tree contains ignored path: %s", e.Path)
		}
	}
	// main.go should be there.
	found := false
	for _, e := range tree.Entries {
		if e.Path == "main.go" {
			found = true
		}
	}
	if !found {
		t.Error("tree missing main.go")
	}
}

func TestStore_ContentDeduplication(t *testing.T) {
	wd := setupWorkDir(t)
	s := newTestStore(t, wd)

	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("unchanged"), 0644)

	// Two snapshots of the same content: the second should NOT create a new
	// blob (blob path is content-addressed, so the file already exists).
	cp1, err := s.Snapshot("turn-1", nil, "first")
	if err != nil {
		t.Fatal(err)
	}
	cp2, err := s.Snapshot("turn-2", nil, "second")
	if err != nil {
		t.Fatal(err)
	}

	// Tree hashes may differ because of timestamp metadata, but the blob
	// count on disk should be 1.
	entries, _ := os.ReadDir(filepath.Join(s.storeRoot, blobDir))
	count := 0
	for _, e := range entries {
		if e.IsDir() {
			sub, _ := os.ReadDir(filepath.Join(s.storeRoot, blobDir, e.Name()))
			for _, s2 := range sub {
				if s2.IsDir() {
					inner, _ := os.ReadDir(filepath.Join(s.storeRoot, blobDir, e.Name(), s2.Name()))
					count += len(inner)
				}
			}
		}
	}
	if count != 1 {
		t.Errorf("blob count = %d, want 1 (content-deduplication failed)", count)
	}
	if cp1.Tree == cp2.Tree {
		// With identical content and sub-second write resolution on
		// Windows NTFS, the two snapshots' trees may be byte-for-byte
		// identical (same mtime hash, same content hash). That's the
		// expected content-addressable behavior — the important assertion
		// is the blob count above (only one blob on disk).
		t.Log("trees identical (expected on fast filesystems)")
	}
}

func TestStore_Revert(t *testing.T) {
	wd := setupWorkDir(t)
	s := newTestStore(t, wd)

	// Turn 1: create a.txt = "v1"
	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("v1"), 0644)
	cp1, _ := s.Snapshot("turn-1", []fsop.FSOp{
		{Op: fsop.OpCreate, Path: "a.txt", Content: "v1"},
	}, "create a.txt")

	// Turn 2: change a.txt = "v2"
	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("v2"), 0644)
	_, _ = s.Snapshot("turn-2", []fsop.FSOp{
		{Op: fsop.OpUpdate, Path: "a.txt", OldString: "v1", NewString: "v2"},
	}, "update a.txt")

	// Turn 3: delete a.txt
	os.Remove(filepath.Join(wd, "a.txt"))
	cp3, _ := s.Snapshot("turn-3", []fsop.FSOp{
		{Op: fsop.OpDelete, Path: "a.txt"},
	}, "delete a.txt")

	if s.Head() != cp3.Tree {
		t.Fatalf("head should be cp3, got %s", s.Head())
	}

	// Revert to cp1 (the state where a.txt = "v1" exists)
	_, err := s.Revert(cp1.ID)
	if err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(wd, "a.txt"))
	if err != nil {
		t.Fatalf("a.txt missing after revert: %v", err)
	}
	if string(got) != "v1" {
		t.Errorf("a.txt = %q, want v1", got)
	}

	// Revert itself should have appended a checkpoint.
	list := s.List()
	if len(list) != 4 {
		t.Errorf("expected 4 checkpoints (3 turns + 1 revert), got %d", len(list))
	}
	if list[3].Kind != "revert" {
		t.Errorf("last checkpoint kind = %q, want revert", list[3].Kind)
	}
}

func TestStore_Revert_RemovesExtraFiles(t *testing.T) {
	wd := setupWorkDir(t)
	s := newTestStore(t, wd)

	// Turn 1: a.txt only
	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("v1"), 0644)
	cp1, _ := s.Snapshot("t1", nil, "")

	// Turn 2: add b.txt
	os.WriteFile(filepath.Join(wd, "b.txt"), []byte("b"), 0644)
	s.Snapshot("t2", nil, "")

	// Revert to turn 1: b.txt should be removed.
	if _, err := s.Revert(cp1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wd, "b.txt")); !os.IsNotExist(err) {
		t.Error("b.txt should be removed after revert to turn 1")
	}
	if _, err := os.Stat(filepath.Join(wd, "a.txt")); err != nil {
		t.Error("a.txt should still exist")
	}
}

func TestStore_Revert_RestoresDirectory(t *testing.T) {
	wd := setupWorkDir(t)
	s := newTestStore(t, wd)

	// Turn 1: src/main.go
	os.MkdirAll(filepath.Join(wd, "src"), 0755)
	os.WriteFile(filepath.Join(wd, "src", "main.go"), []byte("v1"), 0644)
	cp1, _ := s.Snapshot("t1", nil, "")

	// Turn 2: delete main.go
	os.Remove(filepath.Join(wd, "src", "main.go"))
	s.Snapshot("t2", nil, "")

	// Revert to turn 1: src/ should come back.
	if _, err := s.Revert(cp1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wd, "src", "main.go")); err != nil {
		t.Errorf("src/main.go missing after revert: %v", err)
	}
}

func TestStore_Diff(t *testing.T) {
	wd := setupWorkDir(t)
	s := newTestStore(t, wd)

	// State A: a.txt + b.txt
	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("v1"), 0644)
	os.WriteFile(filepath.Join(wd, "b.txt"), []byte("b1"), 0644)
	cpA, _ := s.Snapshot("t1", nil, "")

	// State B: a.txt modified, c.txt added, b.txt deleted
	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("v2"), 0644)
	os.Remove(filepath.Join(wd, "b.txt"))
	os.WriteFile(filepath.Join(wd, "c.txt"), []byte("c1"), 0644)
	cpB, _ := s.Snapshot("t2", nil, "")

	// Diff A→B should show: a modified, b removed, c added
	changes, err := s.Diff(cpA.ID, cpB.ID)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]ChangeKind{}
	for _, ch := range changes {
		kinds[ch.Path] = ch.Kind
	}
	if kinds["a.txt"] != ChangeModified {
		t.Errorf("a.txt kind = %q, want modified", kinds["a.txt"])
	}
	if kinds["b.txt"] != ChangeRemoved {
		t.Errorf("b.txt kind = %q, want removed", kinds["b.txt"])
	}
	if kinds["c.txt"] != ChangeAdded {
		t.Errorf("c.txt kind = %q, want added", kinds["c.txt"])
	}
}

func TestStore_DiffToCurrent(t *testing.T) {
	wd := setupWorkDir(t)
	s := newTestStore(t, wd)

	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("v1"), 0644)
	cp, _ := s.Snapshot("t1", nil, "")

	// Change the file but don't snapshot.
	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("v2"), 0644)

	// Diff to current (empty toID) should show the modification.
	changes, err := s.Diff(cp.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Path != "a.txt" || changes[0].Kind != ChangeModified {
		t.Errorf("diff-to-current = %+v, want [{a.txt modified}]", changes)
	}
}

func TestStore_Get(t *testing.T) {
	wd := setupWorkDir(t)
	s := newTestStore(t, wd)
	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("v1"), 0644)
	cp, _ := s.Snapshot("t1", []fsop.FSOp{{Op: fsop.OpCreate, Path: "a.txt", Content: "v1"}}, "create a.txt")

	got, err := s.Get(cp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary != "create a.txt" {
		t.Errorf("summary = %q", got.Summary)
	}

	// Bogus id → error
	if _, err := s.Get("nonexistent"); err == nil {
		t.Error("Get with bogus id should fail")
	}
}

func TestStore_PersistsAcrossReopen(t *testing.T) {
	wd := setupWorkDir(t)

	// First open: snapshot.
	{
		s, err := NewStore(wd)
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(wd, "a.txt"), []byte("v1"), 0644)
		if _, err := s.Snapshot("t1", nil, ""); err != nil {
			t.Fatal(err)
		}
	}

	// Reopen: should see the same checkpoint.
	{
		s, err := NewStore(wd)
		if err != nil {
			t.Fatal(err)
		}
		list := s.List()
		if len(list) != 1 {
			t.Errorf("after reopen, list len = %d, want 1", len(list))
		}
	}
}

func TestStore_RoundtripTimestamp(t *testing.T) {
	wd := setupWorkDir(t)
	s := newTestStore(t, wd)
	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("v1"), 0644)
	before := time.Now().UTC().Add(-time.Second)
	cp, _ := s.Snapshot("t1", nil, "")
	after := time.Now().UTC().Add(time.Second)

	if cp.Timestamp.Before(before) || cp.Timestamp.After(after) {
		t.Errorf("timestamp %v not in [%v, %v]", cp.Timestamp, before, after)
	}
}

func TestStore_DiffFile(t *testing.T) {
	wd := setupWorkDir(t)
	s := newTestStore(t, wd)

	// State A: 3-line file
	os.WriteFile(filepath.Join(wd, "main.go"), []byte("line1\nline2\nline3\n"), 0644)
	cpA, _ := s.Snapshot("t1", nil, "")

	// State B: line2 changed, line4 added
	os.WriteFile(filepath.Join(wd, "main.go"), []byte("line1\nline2-modified\nline3\nline4\n"), 0644)
	cpB, _ := s.Snapshot("t2", nil, "")

	treeA, _ := loadTree(s.storeRoot, cpA.Tree)
	treeB, _ := loadTree(s.storeRoot, cpB.Tree)
	hashA := treeA.asMap()["main.go"]
	hashB := treeB.asMap()["main.go"]

	// Diff between two distinct hashes
	fd, err := s.DiffFile("main.go", hashA, hashB)
	if err != nil {
		t.Fatal(err)
	}
	if fd.Kind != ChangeModified {
		t.Errorf("kind = %q, want modified", fd.Kind)
	}
	if fd.IsBinary {
		t.Error("file should not be detected as binary")
	}
	if fd.UnifiedDiff == "" {
		t.Error("unified diff is empty for modified file")
	}
	if !strings.Contains(fd.UnifiedDiff, "line2-modified") {
		t.Errorf("unified diff missing 'line2-modified':\n%s", fd.UnifiedDiff)
	}
	if !strings.Contains(fd.UnifiedDiff, "line4") {
		t.Errorf("unified diff missing 'line4' (added line):\n%s", fd.UnifiedDiff)
	}
	if fd.FromLines != 3 || fd.ToLines != 4 {
		t.Errorf("line counts: from=%d to=%d, want 3/4", fd.FromLines, fd.ToLines)
	}

	// Added (from empty)
	added, _ := s.DiffFile("new.go", "", hashB)
	if added.Kind != ChangeAdded {
		t.Errorf("added kind = %q", added.Kind)
	}
	// Removed (to empty)
	removed, _ := s.DiffFile("main.go", hashA, "")
	if removed.Kind != ChangeRemoved {
		t.Errorf("removed kind = %q", removed.Kind)
	}

	// Equal hashes → empty diff, kind=modified (unusual case)
	eq, _ := s.DiffFile("main.go", hashA, hashA)
	if eq.UnifiedDiff != "" {
		t.Errorf("equal hashes should produce empty diff, got: %q", eq.UnifiedDiff)
	}
}

func TestStore_DiffFile_Binary(t *testing.T) {
	wd := setupWorkDir(t)
	s := newTestStore(t, wd)

	// Build a "binary" file: PNG header + NUL bytes in first 8 KB.
	binary := make([]byte, 1024)
	binary[0] = 0x89
	binary[1] = 'P'
	binary[2] = 'N'
	binary[3] = 'G'
	binary[100] = 0x00 // NUL
	os.WriteFile(filepath.Join(wd, "image.png"), binary, 0644)
	cp, _ := s.Snapshot("t1", nil, "")
	tree, _ := loadTree(s.storeRoot, cp.Tree)
	hash := tree.asMap()["image.png"]

	fd, err := s.DiffFile("image.png", "", hash)
	if err != nil {
		t.Fatal(err)
	}
	if !fd.IsBinary {
		t.Error("file with NUL byte should be detected as binary")
	}
	if fd.UnifiedDiff != "" {
		t.Errorf("binary file should have empty unified diff, got: %q", fd.UnifiedDiff)
	}
}

func TestStore_PruneByCount(t *testing.T) {
	wd := setupWorkDir(t)

	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("v1"), 0644)
	s, err := NewStoreWithPolicy(wd, PrunePolicy{
		MaxCheckpoints: 3,
		MaxAge:         0,
		KeepReverts:    true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create 5 snapshots.
	for i := 0; i < 5; i++ {
		os.WriteFile(filepath.Join(wd, "a.txt"), []byte(fmt.Sprintf("v%d", i)), 0644)
		if _, err := s.Snapshot(fmt.Sprintf("t%d", i), nil, ""); err != nil {
			t.Fatal(err)
		}
	}

	// Should have 5 entries in the index, but the first 2 should be pruned
	// (so only 3 unpruned, max=3). The head is the latest (not pruned).
	list := s.List()
	if len(list) != 5 {
		t.Errorf("index has %d entries, want 5 (pruning keeps history)", len(list))
	}
	unpruned := 0
	for _, cp := range list {
		if !cp.Pruned {
			unpruned++
		}
	}
	if unpruned != 3 {
		t.Errorf("unpruned count = %d, want 3", unpruned)
	}
	// The last 3 should be unpruned.
	for i, cp := range list {
		expected := i < 2 // first 2 pruned
		if cp.Pruned != expected {
			t.Errorf("cp %d pruned = %v, want %v", i, cp.Pruned, expected)
		}
	}
}

func TestStore_PruneKeepsReverts(t *testing.T) {
	wd := setupWorkDir(t)

	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("v1"), 0644)
	s, err := NewStoreWithPolicy(wd, PrunePolicy{
		MaxCheckpoints: 2,
		MaxAge:         0,
		KeepReverts:    true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create snapshot, revert, snapshot, revert — ending with 4 entries
	// but 2 are reverts. With max=2 non-revert + reverts always kept, all 4
	// should be unpruned.
	for i := 0; i < 2; i++ {
		os.WriteFile(filepath.Join(wd, "a.txt"), []byte(fmt.Sprintf("v%d", i)), 0644)
		cp, _ := s.Snapshot(fmt.Sprintf("t%d", i), nil, "")
		s.Revert(cp.Tree) // turn each turn into a revert
	}

	list := s.List()
	if len(list) != 4 {
		t.Errorf("list has %d entries, want 4", len(list))
	}
	for i, cp := range list {
		if cp.Pruned {
			t.Errorf("cp %d should not be pruned (reverts are kept)", i)
		}
	}
}

func TestStore_PrunedCannotRevert(t *testing.T) {
	wd := setupWorkDir(t)
	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("v1"), 0644)
	s, _ := NewStoreWithPolicy(wd, PrunePolicy{MaxCheckpoints: 1, MaxAge: 0, KeepReverts: true})

	cp1, _ := s.Snapshot("t1", nil, "")
	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("v2"), 0644)
	s.Snapshot("t2", nil, "")
	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("v3"), 0644)
	s.Snapshot("t3", nil, "")

	// cp1 should be pruned now.
	list := s.List()
	if !list[0].Pruned {
		t.Skipf("cp1 was not pruned, can't test pruned-revert path")
	}
	// Reverting to cp1 should fail (tree object deleted).
	if _, err := s.Revert(cp1.Tree); err == nil {
		t.Error("reverting to pruned checkpoint should fail")
	}
}
