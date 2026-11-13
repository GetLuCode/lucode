package fsop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- Validate ---

func TestValidate(t *testing.T) {
	b := true
	good := []FSOp{
		{Op: OpCreate, Path: "a.txt", Content: "x"},
		{Op: OpUpdate, Path: "a.txt", OldString: "x", NewString: "y"},
		{Op: OpUpdate, Path: "a.txt", OldString: "x", NewString: "y", ReplaceAll: &b},
		{Op: OpUpdate, Path: "a.txt", OldString: "x", NewString: "y", OccurrenceIndex: ptrInt(2)},
		{Op: OpDelete, Path: "a.txt"},
		{Op: OpRename, From: "a.txt", To: "b.txt"},
		{Op: OpCopy, From: "a.txt", To: "c.txt"},
	}
	for i, op := range good {
		if err := op.Validate(); err != nil {
			t.Errorf("op %d (%s) should be valid, got: %v", i, op.Op, err)
		}
	}

	bad := []FSOp{
		{Op: OpCreate},                              // missing path
		{Op: OpUpdate, Path: "a.txt"},               // missing old_string
		{Op: OpUpdate, Path: "a.txt", OldString: ""}, // empty old_string
		{Op: OpUpdate, Path: "a.txt", OldString: "x", NewString: "y", OccurrenceIndex: ptrInt(0)}, // 0 index
		{Op: OpDelete},                              // missing path
		{Op: OpRename, From: "a.txt"},               // missing to
		{Op: OpRename, From: "a", To: "a"},          // from == to
		{Op: "unknown"},                             // unknown op
		{},                                          // empty
	}
	for i, op := range bad {
		if err := op.Validate(); err == nil {
			t.Errorf("op %d (%q) should be invalid, got nil", i, op.Op)
		}
	}
}

// --- ResolvePath ---

func TestResolvePath(t *testing.T) {
	workDir := filepath.FromSlash("/home/user/proj")
	tests := []struct {
		input     string
		want      string
		wantError bool
	}{
		{"foo.txt", filepath.Join(workDir, "foo.txt"), false},
		{"src/main.go", filepath.Join(workDir, "src", "main.go"), false},
		{"", workDir, false},
		{"/etc/passwd", filepath.FromSlash("/etc/passwd"), true}, // absolute, but not under workDir
		{"/home/user/proj/foo.txt", filepath.FromSlash("/home/user/proj/foo.txt"), false},
		{"../escape.txt", "", true},            // relative escape
		{"/home/user/other/foo.txt", "", true}, // absolute, different parent
	}
	for _, tc := range tests {
		got, err := ResolvePath(tc.input, workDir)
		if tc.wantError {
			if err == nil {
				t.Errorf("ResolvePath(%q, %q) = %q, want error", tc.input, workDir, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ResolvePath(%q, %q) error: %v", tc.input, workDir, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ResolvePath(%q, %q) = %q, want %q", tc.input, workDir, got, tc.want)
		}
	}
}

// --- IgnoreSet ---

func TestIgnoreSet_Defaults(t *testing.T) {
	workDir, _ := os.MkdirTemp("", "fsop-ignore")
	defer os.RemoveAll(workDir)
	ig, err := NewIgnoreSet(workDir)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path string
		want bool
	}{
		{"src/main.go", false},
		{"main.go", false},
		{"node_modules", true},
		{"node_modules/lodash/index.js", true},
		{"packages/app/node_modules/react/index.js", true},
		{"dist", true},
		{"dist/bundle.js", true},
		{".git/HEAD", true},
		{".lucode/checkpoints/x", true},
		{".next/cache/abc", true},
		{"foo.pyc", false}, // *.pyc is in defaults but the test below covers it via .agentignore
	}
	for _, tc := range tests {
		got := ig.IsIgnored(tc.path)
		if got != tc.want {
			t.Errorf("IsIgnored(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestIgnoreSet_Agentignore(t *testing.T) {
	workDir, _ := os.MkdirTemp("", "fsop-ignore")
	defer os.RemoveAll(workDir)

	os.WriteFile(filepath.Join(workDir, ".agentignore"), []byte(`
# comment
*.log
build-output/
!build-output/keep.txt
`), 0644)

	ig, err := NewIgnoreSet(workDir)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path string
		want bool
	}{
		{"app.log", true},              // glob from .agentignore
		{"logs/app.log", true},          // glob matches basename anywhere
		{"build-output/x", true},       // dir from .agentignore
		{"build-output/keep.txt", true}, // negation not supported in v1, so still ignored
		{"src/main.go", false},         // not in defaults or .agentignore
		{"node_modules/foo", true},     // still matches default
	}
	for _, tc := range tests {
		got := ig.IsIgnored(tc.path)
		if got != tc.want {
			t.Errorf("IsIgnored(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// --- ComputeHead ---

func TestComputeHead(t *testing.T) {
	workDir, _ := os.MkdirTemp("", "fsop-head")
	defer os.RemoveAll(workDir)

	os.WriteFile(filepath.Join(workDir, "a.txt"), []byte("hello"), 0644)
	os.MkdirAll(filepath.Join(workDir, "src"), 0755)
	os.WriteFile(filepath.Join(workDir, "src", "main.go"), []byte("package main"), 0644)
	os.MkdirAll(filepath.Join(workDir, "node_modules", "react"), 0755)
	os.WriteFile(filepath.Join(workDir, "node_modules", "react", "index.js"), []byte("..."), 0644)

	ig, _ := NewIgnoreSet(workDir)
	h1, err := ComputeHead(workDir, ig)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == "" {
		t.Fatal("head is empty")
	}

	// Touch a.txt: head should change.
	future := time.Unix(2_000_000_000, 0)
	os.Chtimes(filepath.Join(workDir, "a.txt"), future, future)
	h2, _ := ComputeHead(workDir, ig)
	if h1 == h2 {
		t.Errorf("head did not change after touching a.txt: %s == %s", h1, h2)
	}

	// Touch node_modules: head should NOT change (ignored).
	os.Chtimes(filepath.Join(workDir, "node_modules", "react", "index.js"), future, future)
	h3, _ := ComputeHead(workDir, ig)
	if h2 != h3 {
		t.Errorf("head changed when only ignored file was touched: %s != %s", h2, h3)
	}
}

// --- ApplyBatch ---

func setupWorkDir(t *testing.T) string {
	t.Helper()
	dir, _ := os.MkdirTemp("", "fsop-apply")
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestApplyBatch_Create(t *testing.T) {
	wd := setupWorkDir(t)
	ig, _ := NewIgnoreSet(wd)

	ops := []FSOp{{Op: OpCreate, Path: "a.txt", Content: "hello"}}
	res, err := ApplyBatch(ops, wd, ig, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.OpsRun != 1 {
		t.Errorf("OpsRun = %d, want 1", res.OpsRun)
	}
	if res.NewHead == "" {
		t.Error("NewHead is empty")
	}
	got, _ := os.ReadFile(filepath.Join(wd, "a.txt"))
	if string(got) != "hello" {
		t.Errorf("file content = %q, want %q", got, "hello")
	}
}

func TestApplyBatch_Update(t *testing.T) {
	wd := setupWorkDir(t)
	ig, _ := NewIgnoreSet(wd)
	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("foo bar baz"), 0644)

	// Plain update, unique match
	ops := []FSOp{{Op: OpUpdate, Path: "a.txt", OldString: "bar", NewString: "BAR"}}
	if _, err := ApplyBatch(ops, wd, ig, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(wd, "a.txt"))
	if string(got) != "foo BAR baz" {
		t.Errorf("after update = %q, want %q", got, "foo BAR baz")
	}
}

func TestApplyBatch_Update_AmbiguousFails(t *testing.T) {
	wd := setupWorkDir(t)
	ig, _ := NewIgnoreSet(wd)
	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("x x x"), 0644)

	ops := []FSOp{{Op: OpUpdate, Path: "a.txt", OldString: "x", NewString: "y"}}
	_, err := ApplyBatch(ops, wd, ig, "")
	if err == nil || !strings.Contains(err.Error(), "appears 3 times") {
		t.Errorf("expected ambiguity error, got: %v", err)
	}
	// File should be untouched.
	got, _ := os.ReadFile(filepath.Join(wd, "a.txt"))
	if string(got) != "x x x" {
		t.Errorf("file changed on failed update: %q", got)
	}
}

func TestApplyBatch_Update_OccurrenceIndex(t *testing.T) {
	wd := setupWorkDir(t)
	ig, _ := NewIgnoreSet(wd)
	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("x x x"), 0644)

	ops := []FSOp{{Op: OpUpdate, Path: "a.txt", OldString: "x", NewString: "y", OccurrenceIndex: ptrInt(2)}}
	if _, err := ApplyBatch(ops, wd, ig, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(wd, "a.txt"))
	if string(got) != "x y x" {
		t.Errorf("after occurrence_index=2 update = %q, want %q", got, "x y x")
	}
}

func TestApplyBatch_Update_ReplaceAll(t *testing.T) {
	wd := setupWorkDir(t)
	ig, _ := NewIgnoreSet(wd)
	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("x x x"), 0644)

	b := true
	ops := []FSOp{{Op: OpUpdate, Path: "a.txt", OldString: "x", NewString: "y", ReplaceAll: &b}}
	if _, err := ApplyBatch(ops, wd, ig, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(wd, "a.txt"))
	if string(got) != "y y y" {
		t.Errorf("after replace_all = %q, want %q", got, "y y y")
	}
}

func TestApplyBatch_DeleteRenameCopy(t *testing.T) {
	wd := setupWorkDir(t)
	ig, _ := NewIgnoreSet(wd)
	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("hello"), 0644)

	// Copy a -> b
	ops := []FSOp{{Op: OpCopy, From: "a.txt", To: "b.txt"}}
	if _, err := ApplyBatch(ops, wd, ig, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wd, "b.txt")); err != nil {
		t.Errorf("b.txt not created: %v", err)
	}

	// Rename b -> c
	ops = []FSOp{{Op: OpRename, From: "b.txt", To: "c.txt"}}
	if _, err := ApplyBatch(ops, wd, ig, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wd, "b.txt")); !os.IsNotExist(err) {
		t.Error("b.txt should be gone after rename")
	}
	if _, err := os.Stat(filepath.Join(wd, "c.txt")); err != nil {
		t.Errorf("c.txt not created: %v", err)
	}

	// Delete c
	ops = []FSOp{{Op: OpDelete, Path: "c.txt"}}
	if _, err := ApplyBatch(ops, wd, ig, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wd, "c.txt")); !os.IsNotExist(err) {
		t.Error("c.txt should be gone after delete")
	}
}

func TestApplyBatch_Atomicity(t *testing.T) {
	wd := setupWorkDir(t)
	ig, _ := NewIgnoreSet(wd)
	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("original"), 0644)

	// First op succeeds (update a.txt), second op fails (delete non-existent).
	ops := []FSOp{
		{Op: OpUpdate, Path: "a.txt", OldString: "original", NewString: "CHANGED"},
		{Op: OpDelete, Path: "nonexistent.txt"},
	}
	_, err := ApplyBatch(ops, wd, ig, "")
	if err == nil {
		t.Fatal("expected error from second op")
	}
	if !strings.Contains(err.Error(), "op 1") {
		t.Errorf("error should mention op 1, got: %v", err)
	}
	// a.txt should be rolled back to "original".
	got, _ := os.ReadFile(filepath.Join(wd, "a.txt"))
	if string(got) != "original" {
		t.Errorf("a.txt not rolled back: %q, want %q", got, "original")
	}
}

func TestApplyBatch_StaleHead(t *testing.T) {
	wd := setupWorkDir(t)
	ig, _ := NewIgnoreSet(wd)

	// Use explicit mtimes so the test isn't sensitive to filesystem time
	// resolution (Windows NTFS rounds to ~100ns; some filesystems round to
	// 2s, which would break an mtime-based test).
	past := time.Unix(1_000_000_000, 0)
	future := time.Unix(2_000_000_000, 0)
	os.WriteFile(filepath.Join(wd, "a.txt"), []byte("v1"), 0644)
	os.Chtimes(filepath.Join(wd, "a.txt"), past, past)

	// First call: succeeds, returns new head.
	res1, err := ApplyBatch([]FSOp{{Op: OpCreate, Path: "b.txt", Content: "x"}}, wd, ig, "")
	if err != nil {
		t.Fatal(err)
	}

	// Now someone changes a.txt (external mutation) with a known new mtime.
	os.Chtimes(filepath.Join(wd, "a.txt"), future, future)

	// Second call with the previous head: should fail (stale).
	ops := []FSOp{{Op: OpCreate, Path: "c.txt", Content: "y"}}
	_, err = ApplyBatch(ops, wd, ig, res1.NewHead)
	if err == nil || !strings.Contains(err.Error(), "stale head checkpoint") {
		t.Errorf("expected stale head error, got: %v", err)
	}
	// c.txt should NOT have been created.
	if _, err := os.Stat(filepath.Join(wd, "c.txt")); !os.IsNotExist(err) {
		t.Error("c.txt should not exist after rejected call")
	}
}

func TestApplyBatch_HeadConsistent(t *testing.T) {
	wd := setupWorkDir(t)
	ig, _ := NewIgnoreSet(wd)

	// Two consecutive successful calls: second's head should differ from first's.
	_, err := ApplyBatch([]FSOp{{Op: OpCreate, Path: "a.txt", Content: "x"}}, wd, ig, "")
	if err != nil {
		t.Fatal(err)
	}
	h1, _ := ComputeHead(wd, ig)

	_, err = ApplyBatch([]FSOp{{Op: OpCreate, Path: "b.txt", Content: "y"}}, wd, ig, h1)
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := ComputeHead(wd, ig)
	if h1 == h2 {
		t.Error("head did not change between batches")
	}
}

// --- replaceNth ---

func TestReplaceNth(t *testing.T) {
	tests := []struct {
		s, old, new string
		n           int
		want        string
	}{
		{"x x x", "x", "y", 1, "y x x"},
		{"x x x", "x", "y", 2, "x y x"},
		{"x x x", "x", "y", 3, "x x y"},
		{"abc abc abc", "abc", "X", 2, "abc X abc"},
	}
	for _, tc := range tests {
		got := replaceNth(tc.s, tc.old, tc.new, tc.n)
		if got != tc.want {
			t.Errorf("replaceNth(%q, %q, %q, %d) = %q, want %q", tc.s, tc.old, tc.new, tc.n, got, tc.want)
		}
	}
}

// --- JSON serialization of FSOp (round-trip) ---

func TestFSOp_JSON(t *testing.T) {
	b := true
	n := 3
	op := FSOp{
		Op:              OpUpdate,
		Path:            "a.txt",
		OldString:       "x",
		NewString:       "y",
		ReplaceAll:      &b,
		OccurrenceIndex: &n,
	}
	data, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	// Verify pointers serialize correctly.
	if !strings.Contains(string(data), `"replace_all":true`) {
		t.Errorf("replace_all not serialized as true: %s", data)
	}
	if !strings.Contains(string(data), `"occurrence_index":3`) {
		t.Errorf("occurrence_index not serialized as 3: %s", data)
	}

	var back FSOp
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.ReplaceAll == nil || !*back.ReplaceAll {
		t.Error("ReplaceAll not round-tripped")
	}
	if back.OccurrenceIndex == nil || *back.OccurrenceIndex != 3 {
		t.Error("OccurrenceIndex not round-tripped")
	}
}

func ptrInt(i int) *int { return &i }
