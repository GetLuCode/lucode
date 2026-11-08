package main

import (
	"archive/zip"
	"bytes"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The plugin new starts must test, pack and validate as it is, and its zip must hold what the
// hub runs and nothing of the toolchain.
func TestNewTestPack(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "notes")
	var out bytes.Buffer
	if err := run([]string{"new", "acme.notes", "-dir", dir}, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{"test", dir}, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "3 passed, 0 failed") {
		t.Fatalf("%s", out.String())
	}

	dest := filepath.Join(t.TempDir(), "notes.zip")
	out.Reset()
	if err := run([]string{"pack", dir, "-o", dest}, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	zr, err := zip.OpenReader(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	want := []string{"README.md", "assets/icon.svg", "dist/main.css", "dist/main.js", "dist/server.js", "plugin.json"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("zip:\n  %v\nwant\n  %v", names, want)
	}
	if err := run([]string{"validate", dest}, &out); err != nil {
		t.Fatal(err)
	}
}

func TestNewPlainStillWorks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hello")
	var out bytes.Buffer
	if err := run([]string{"new", "acme.hello", "-dir", dir, "-plain"}, &out); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"pack", dir, "-o", filepath.Join(t.TempDir(), "h.zip")}, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
}

func TestValidateSaysToBuild(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "notes")
	var out bytes.Buffer
	if err := run([]string{"new", "acme.notes", "-dir", dir}, &out); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"validate", dir}, &out)
	if err == nil || !strings.Contains(err.Error(), "run lucode-plugin build") {
		t.Fatalf("validate before a build: %v", err)
	}
}
