package stt

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// home points USERPROFILE at a folder inside the project (lucode/tmp), not the system temp dir.
func home(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "tmp"))
	if err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(root, 0o755)
	dir, err := os.MkdirTemp(root, "sttmodel")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	return dir
}

func TestSelectedPrecedence(t *testing.T) {
	home(t)
	t.Setenv("LUCODE_STT_ENGINE", "")
	if got := Selected(); got != "nemo-fp16" {
		t.Fatalf("default: got %q, want nemo-fp16", got)
	}
	if err := saveSelected("sherpa-int8"); err != nil {
		t.Fatal(err)
	}
	if got := Selected(); got != "sherpa-int8" {
		t.Fatalf("saved choice: got %q", got)
	}
	// the environment wins over the saved choice, and understands the older short names
	for env, want := range map[string]string{"nemo": "nemo-fp16", "sherpa": "sherpa-int8", "nemo-fp16": "nemo-fp16"} {
		t.Setenv("LUCODE_STT_ENGINE", env)
		if got := Selected(); got != want {
			t.Errorf("LUCODE_STT_ENGINE=%q: got %q, want %q", env, got, want)
		}
	}
}

func TestSelectedIgnoresUnknownSavedModel(t *testing.T) {
	dir := home(t)
	t.Setenv("LUCODE_STT_ENGINE", "")
	_ = os.MkdirAll(filepath.Join(dir, ".lucode"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, ".lucode", "stt-model.json"), []byte(`{"model":"made-up"}`), 0o644)
	if got := Selected(); got != "nemo-fp16" {
		t.Fatalf("got %q, want the default for an unknown saved model", got)
	}
}

func TestModelArgs(t *testing.T) {
	cases := map[string][]string{
		"nemo-fp16":   {"--engine-key", "nemo-fp16", "--engine", "nemo", "--fp16"},
		"nemo":        {"--engine-key", "nemo", "--engine", "nemo"},
		"sherpa-int8": {"--engine-key", "sherpa-int8", "--engine", "sherpa", "--model-dir", "M"},
	}
	for id, want := range cases {
		if got := modelArgs(id, "M"); !slices.Equal(got, want) {
			t.Errorf("%s: got %v, want %v", id, got, want)
		}
	}
	// every listed model has args that report its own id as the engine key
	for _, m := range Models {
		a := modelArgs(m.ID, "M")
		if i := slices.Index(a, "--engine-key"); i < 0 || a[i+1] != m.ID {
			t.Errorf("%s: engine key in %v does not match the model id", m.ID, a)
		}
	}
}

func TestSwitchRejectsUnknownModel(t *testing.T) {
	home(t)
	if _, err := Switch(t.Context(), "nope"); err == nil {
		t.Fatal("an unknown model must be rejected")
	}
	if _, err := os.Stat(modelFile()); err == nil {
		t.Fatal("an unknown model must not be saved")
	}
}
