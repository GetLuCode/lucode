package stt

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestPackTarGzKeepsFolderAndContent(t *testing.T) {
	root, err := os.MkdirTemp(".", "tmp-pack-") // inside the package folder, removed at the end
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	src := filepath.Join(root, "model")
	if err := os.MkdirAll(filepath.Join(src, "am"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "am", "final.mdl"), []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "README"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "model.tar.gz")
	if err := packTarGz(src, dst, "vosk-model-x"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dst + ".part"); err == nil {
		t.Fatal("the .part file was left behind")
	}
	f, err := os.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		got[h.Name] = string(b)
	}
	if got["vosk-model-x/am/final.mdl"] != "abc" || got["vosk-model-x/README"] != "hi" {
		t.Fatalf("unexpected archive content: %v", got)
	}
	if _, ok := got["vosk-model-x/am/"]; !ok {
		t.Fatalf("the folder entry is missing: %v", got)
	}
}
