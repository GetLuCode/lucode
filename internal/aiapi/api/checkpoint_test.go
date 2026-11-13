package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"claude-orchestrator/internal/aiapi/executor/checkpoint"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	return NewServer("MiniMax-M3")
}

func TestCheckpointAPI_ListEmpty(t *testing.T) {
	wd, _ := os.MkdirTemp("", "ck-test")
	defer os.RemoveAll(wd)

	srv := newTestServer(t)
	req := httptest.NewRequest("GET", "/api/checkpoints?work_dir="+wd, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	cps, _ := resp["checkpoints"].([]any)
	if len(cps) != 0 {
		t.Errorf("expected empty checkpoint list, got %d", len(cps))
	}
}

func TestCheckpointAPI_MissingWorkDir(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest("GET", "/api/checkpoints", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestCheckpointAPI_FullCycle(t *testing.T) {
	wd, _ := os.MkdirTemp("", "ck-test")
	defer os.RemoveAll(wd)

	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("v1"), 0644)

	srv := newTestServer(t)

	// 1. List empty.
	{
		req := httptest.NewRequest("GET", "/api/checkpoints?work_dir="+wd, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("list status = %d", rec.Code)
		}
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		if cps := resp["checkpoints"].([]any); len(cps) != 0 {
			t.Fatalf("expected 0 checkpoints, got %d", len(cps))
		}
	}

	// 2. Manually create a snapshot.
	store, err := checkpoint.NewStore(wd)
	if err != nil {
		t.Fatal(err)
	}
	cp1, err := store.Snapshot("turn-1", nil, "create a.txt=v1")
	if err != nil {
		t.Fatal(err)
	}

	// 3. List — should have 1.
	{
		req := httptest.NewRequest("GET", "/api/checkpoints?work_dir="+wd, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		if cps := resp["checkpoints"].([]any); len(cps) != 1 {
			t.Fatalf("expected 1 checkpoint, got %d", len(cps))
		}
		if resp["head"].(string) != cp1.Tree {
			t.Errorf("head = %q, want %q", resp["head"], cp1.Tree)
		}
	}

	// 4. Get one.
	{
		req := httptest.NewRequest("GET", "/api/checkpoints/"+cp1.Tree+"?work_dir="+wd, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("get status = %d; body: %s", rec.Code, rec.Body.String())
		}
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		if _, ok := resp["tree"]; !ok {
			t.Error("response missing tree")
		}
	}

	// 5. Mutate workDir, then diff (cp1 → current).
	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("v2"), 0644)
	os.WriteFile(filepath.Join(wd, "b.txt"), []byte("b"), 0644)
	{
		req := httptest.NewRequest("GET", "/api/diff?work_dir="+wd+"&from="+cp1.Tree, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("diff status = %d; body: %s", rec.Code, rec.Body.String())
		}
		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)
		changes := resp["changes"].([]any)
		if len(changes) != 2 {
			t.Errorf("expected 2 changes, got %d", len(changes))
		}
	}

	// 6. Revert to cp1.
	{
		req := httptest.NewRequest("POST", "/api/revert/"+cp1.Tree+"?work_dir="+wd, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("revert status = %d; body: %s", rec.Code, rec.Body.String())
		}
	}

	// 7. After revert, a.txt=v1 and b.txt is gone.
	got, _ := os.ReadFile(filepath.Join(wd, "a.txt"))
	if string(got) != "v1" {
		t.Errorf("a.txt after revert = %q, want v1", got)
	}
	if _, err := os.Stat(filepath.Join(wd, "b.txt")); !os.IsNotExist(err) {
		t.Error("b.txt should be removed after revert")
	}
}

func TestCheckpointAPI_BlobFetch(t *testing.T) {
	wd, _ := os.MkdirTemp("", "ck-test")
	defer os.RemoveAll(wd)
	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("hello world"), 0644)

	store, _ := checkpoint.NewStore(wd)
	cp, _ := store.Snapshot("t1", nil, "")

	srv := newTestServer(t)
	tree, _ := store.LoadTree(cp.Tree)
	var hash string
	for _, e := range tree.Entries {
		if e.Path == "a.txt" {
			hash = e.Hash
			break
		}
	}
	if hash == "" {
		t.Fatal("a.txt not in tree")
	}

	req := httptest.NewRequest("GET", "/api/blob/"+hash+"?work_dir="+wd, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("blob status = %d", rec.Code)
	}
	if rec.Body.String() != "hello world" {
		t.Errorf("blob body = %q, want 'hello world'", rec.Body.String())
	}
}

func TestCheckpointAPI_GetUnknownID(t *testing.T) {
	wd, _ := os.MkdirTemp("", "ck-test")
	defer os.RemoveAll(wd)
	srv := newTestServer(t)
	req := httptest.NewRequest("GET", "/api/checkpoints/nonexistent?work_dir="+wd, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestCheckpointAPI_DiffFile(t *testing.T) {
	wd, _ := os.MkdirTemp("", "ck-test")
	defer os.RemoveAll(wd)

	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("line1\nline2\nline3\n"), 0644)
	store, _ := checkpoint.NewStore(wd)
	cpA, _ := store.Snapshot("t1", nil, "")

	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("line1\nline2-modified\nline3\nline4\n"), 0644)
	cpB, _ := store.Snapshot("t2", nil, "")

	treeA, _ := store.LoadTree(cpA.Tree)
	treeB, _ := store.LoadTree(cpB.Tree)
	hashA := treeA.Entries[0].Hash
	hashB := treeB.Entries[0].Hash

	srv := newTestServer(t)
	url := "/api/diff/file?work_dir=" + wd + "&path=a.txt&from=" + hashA + "&to=" + hashB
	req := httptest.NewRequest("GET", url, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["kind"] != "modified" {
		t.Errorf("kind = %v, want modified", resp["kind"])
	}
	if resp["unified_diff"] == "" {
		t.Error("unified_diff is empty")
	}
	if resp["from_lines"].(float64) != 3 || resp["to_lines"].(float64) != 4 {
		t.Errorf("line counts: %v / %v", resp["from_lines"], resp["to_lines"])
	}
}

func TestCheckpointAPI_DiffFile_MissingParams(t *testing.T) {
	wd, _ := os.MkdirTemp("", "ck-test")
	defer os.RemoveAll(wd)
	srv := newTestServer(t)

	// Missing path
	req := httptest.NewRequest("GET", "/api/diff/file?work_dir="+wd+"&from=abc", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Errorf("missing path: status = %d, want 400", rec.Code)
	}

	// Missing both from and to
	req = httptest.NewRequest("GET", "/api/diff/file?work_dir="+wd+"&path=a.txt", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Errorf("missing from+to: status = %d, want 400", rec.Code)
	}

	// Missing work_dir
	req = httptest.NewRequest("GET", "/api/diff/file?path=a.txt&from=abc", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Errorf("missing work_dir: status = %d, want 400", rec.Code)
	}
}
