package stt

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// The browser detects the hotword with Vosk compiled to wasm (vosk-browser), which loads its
// model from a .tar.gz with the model folder inside. The model is the same Spanish small model
// the voice worker uses (models\vosk-model-small-es-0.42 in LUCODE_STT_DIR); it is packed once
// into ~/.lucode and served by GET /api/stt/vosk-model.tar.gz.

var archiveMu sync.Mutex

// VoskModelDir is the folder of the Vosk model the worker uses.
func VoskModelDir() string {
	return filepath.Join(envOr("LUCODE_STT_DIR", defaultDir), "models", voskName)
}

// VoskModelArchive returns the path of the model as a .tar.gz, packing it the first time (or
// when the model folder is newer than the archive).
func VoskModelArchive() (string, error) {
	archiveMu.Lock()
	defer archiveMu.Unlock()
	src := VoskModelDir()
	info, err := os.Stat(src)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("vosk model not found at %s (set LUCODE_STT_DIR)", src)
	}
	if err := os.MkdirAll(stateDir(), 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(stateDir(), voskName+".tar.gz")
	if a, err := os.Stat(dst); err == nil && !a.ModTime().Before(info.ModTime()) {
		return dst, nil
	}
	if err := packTarGz(src, dst, voskName); err != nil {
		return "", err
	}
	return dst, nil
}

// packTarGz writes src as a .tar.gz whose entries live under the folder top. It writes next to
// dst and renames, so a request never sees a half-written file.
func packTarGz(src, dst, top string) (err error) {
	part := dst + ".part"
	f, err := os.Create(part)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(part)
		}
	}()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		fi, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if !fi.IsDir() && !fi.Mode().IsRegular() {
			return nil
		}
		h, herr := tar.FileInfoHeader(fi, "")
		if herr != nil {
			return herr
		}
		h.Name = top + "/" + filepath.ToSlash(rel)
		if fi.IsDir() {
			h.Name += "/"
		}
		if werr := tw.WriteHeader(h); werr != nil {
			return werr
		}
		if fi.IsDir() {
			return nil
		}
		in, oerr := os.Open(p)
		if oerr != nil {
			return oerr
		}
		defer in.Close()
		_, cerr := io.Copy(tw, in)
		return cerr
	})
	if err != nil {
		return err
	}
	if err = tw.Close(); err != nil {
		return err
	}
	if err = gz.Close(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(part, dst); err != nil {
		return errors.Join(err, os.Remove(part))
	}
	return nil
}
