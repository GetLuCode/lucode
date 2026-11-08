package plugins

import (
	"archive/zip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// Limits of a package.
const (
	MaxZipBytes      = 50 << 20
	MaxUnpackedBytes = 200 << 20
	MaxZipFiles      = 2000
	MaxFileBytes     = 20 << 20
	maxRatio         = 200 // unpacked to packed, for anything bigger than ratioFloor
	ratioFloor       = 1 << 20
)

var allowedExt = map[string]bool{
	".json": true, ".js": true, ".md": true, ".txt": true, ".html": true, ".css": true,
	".svg": true, ".png": true, ".webp": true, ".jpg": true, ".jpeg": true,
}

var imageExt = map[string]bool{".svg": true, ".png": true, ".webp": true, ".jpg": true, ".jpeg": true}

var reservedName = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[0-9¹²³]|lpt[0-9¹²³])(\..*)?$`)

func isImage(p string) bool { return imageExt[strings.ToLower(path.Ext(p))] }

// checkPluginPath refuses anything that is not a plain relative path with forward slashes.
func checkPluginPath(p string) error {
	switch {
	case p == "":
		return errors.New("empty path")
	case strings.ContainsRune(p, 0):
		return errors.New("NUL in a path")
	case strings.Contains(p, `\`):
		return errors.New("backslash in a path")
	case strings.Contains(p, ":"):
		return errors.New("colon in a path")
	case strings.HasPrefix(p, "/"):
		return errors.New("absolute path")
	}
	for _, part := range strings.Split(p, "/") {
		switch {
		case part == "" || part == "." || part == "..":
			return fmt.Errorf("the path %q leaves the package or is not clean", p)
		case strings.HasSuffix(part, ".") || strings.HasSuffix(part, " "):
			return fmt.Errorf("the path %q ends a name in a dot or a space", p)
		case reservedName.MatchString(part):
			return fmt.Errorf("the path %q uses a name Windows reserves", p)
		case strings.ContainsAny(part, `<>"|?*`):
			return fmt.Errorf("the path %q has characters Windows refuses", p)
		}
	}
	return nil
}

// CheckZip validates an archive without unpacking anything, and returns the manifest. The sizes
// in a zip's headers can lie, so ExtractZip counts again while it writes.
func CheckZip(file string, opts ParseOptions) (Manifest, error) {
	var m Manifest
	info, err := os.Stat(file)
	if err != nil {
		return m, err
	}
	if info.Size() > MaxZipBytes {
		return m, fmt.Errorf("the zip is larger than %d MB", MaxZipBytes>>20)
	}
	r, err := zip.OpenReader(file)
	if err != nil {
		return m, errors.New("this is not a readable zip")
	}
	defer r.Close()
	if len(r.File) > MaxZipFiles {
		return m, fmt.Errorf("the zip has more than %d files", MaxZipFiles)
	}
	seen := map[string]bool{}
	files := map[string]bool{}
	var total uint64
	var manifest *zip.File
	for _, f := range r.File {
		name := strings.TrimSuffix(f.Name, "/")
		if err := checkPluginPath(name); err != nil {
			return m, fmt.Errorf("zip entry %q: %w", f.Name, err)
		}
		mode := f.Mode()
		isDir := strings.HasSuffix(f.Name, "/")
		if !isDir && !mode.IsRegular() {
			return m, fmt.Errorf("zip entry %q is a link or a special file", f.Name)
		}
		if isDir && (mode&os.ModeSymlink != 0) {
			return m, fmt.Errorf("zip entry %q is a link", f.Name)
		}
		fold := strings.ToLower(name)
		if seen[fold] {
			return m, fmt.Errorf("zip entry %q is there twice, ignoring case", f.Name)
		}
		seen[fold] = true
		if isDir {
			continue
		}
		files[name] = true
		ext := strings.ToLower(path.Ext(name))
		if !allowedExt[ext] {
			return m, fmt.Errorf("zip entry %q: files of type %q are not allowed", f.Name, ext)
		}
		if strings.HasPrefix(name, "assets/") && !imageExt[ext] {
			return m, fmt.Errorf("zip entry %q: assets may only be images", f.Name)
		}
		if strings.HasPrefix(name, "blueprints/") && ext != ".json" {
			return m, fmt.Errorf("zip entry %q: blueprints must be .json", f.Name)
		}
		if f.UncompressedSize64 > MaxFileBytes {
			return m, fmt.Errorf("zip entry %q is larger than %d MB", f.Name, MaxFileBytes>>20)
		}
		if f.UncompressedSize64 > ratioFloor && (f.CompressedSize64 == 0 || f.UncompressedSize64/f.CompressedSize64 > maxRatio) {
			return m, fmt.Errorf("zip entry %q compresses too well to be real", f.Name)
		}
		total += f.UncompressedSize64
		if total > MaxUnpackedBytes {
			return m, fmt.Errorf("the zip unpacks to more than %d MB", MaxUnpackedBytes>>20)
		}
		if name == "plugin.json" {
			manifest = f
		}
	}
	// a name cannot be both a file and a folder
	for name := range seen {
		for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
			if lowerHas(files, dir) {
				return m, fmt.Errorf("zip entry %q is both a file and a folder", dir)
			}
		}
	}
	if manifest == nil {
		return m, errors.New("the zip has no plugin.json at its root")
	}
	raw, err := readZipFile(manifest, 1<<20)
	if err != nil {
		return m, fmt.Errorf("plugin.json: %w", err)
	}
	if m, err = ParseManifest(raw, opts); err != nil {
		return m, err
	}
	need := []string{}
	if m.Icon != "" {
		need = append(need, m.Icon)
	}
	for _, t := range []*Target{m.Targets.Server, m.Targets.Runner} {
		if t != nil {
			need = append(need, t.Main)
		}
	}
	for _, d := range m.Contributes.Blueprints {
		need = append(need, d.File)
	}
	for _, d := range m.Contributes.Models {
		if d.Icon != "" {
			need = append(need, d.Icon)
		}
	}
	for _, d := range m.Contributes.Pages {
		if d.Icon != "" {
			need = append(need, d.Icon)
		}
		if d.UI != "" {
			need = append(need, d.UI)
		}
	}
	for _, d := range m.Contributes.FlowNodes {
		if d.Icon != "" {
			need = append(need, d.Icon)
		}
	}
	for _, f := range need {
		if !files[f] {
			return m, fmt.Errorf("plugin.json names %q, which is not in the zip", f)
		}
	}
	return m, nil
}

func lowerHas(files map[string]bool, lowered string) bool {
	for f := range files {
		if strings.ToLower(f) == lowered {
			return true
		}
	}
	return false
}

func readZipFile(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("too large")
	}
	return data, nil
}

// ExtractZip unpacks a checked zip to a new folder next to dest and renames it into place, so
// a failure half way leaves nothing behind. dest must not exist.
func ExtractZip(file, dest string) error {
	r, err := zip.OpenReader(file)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	var tag [6]byte
	_, _ = rand.Read(tag[:])
	work := dest + ".tmp-" + hex.EncodeToString(tag[:])
	if err := os.Mkdir(work, 0o755); err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			os.RemoveAll(work)
		}
	}()
	root, err := os.OpenRoot(work)
	if err != nil {
		return err
	}
	defer root.Close()
	var total int64
	for _, f := range r.File {
		if strings.HasSuffix(f.Name, "/") {
			if err := checkPluginPath(strings.TrimSuffix(f.Name, "/")); err != nil {
				return err
			}
			if err := root.MkdirAll(strings.TrimSuffix(f.Name, "/"), 0o755); err != nil {
				return err
			}
			continue
		}
		if err := checkPluginPath(f.Name); err != nil {
			return err
		}
		if !f.Mode().IsRegular() {
			return fmt.Errorf("zip entry %q is a link or a special file", f.Name)
		}
		if dir := path.Dir(f.Name); dir != "." {
			if err := root.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
		n, err := extractOne(root, f)
		if err != nil {
			return fmt.Errorf("zip entry %q: %w", f.Name, err)
		}
		if total += n; total > MaxUnpackedBytes {
			return fmt.Errorf("the zip unpacks to more than %d MB", MaxUnpackedBytes>>20)
		}
	}
	root.Close()
	if err := os.Rename(work, dest); err != nil {
		return err
	}
	done = true
	return nil
}

func extractOne(root *os.Root, f *zip.File) (int64, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	out, err := root.OpenFile(f.Name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	defer out.Close()
	n, err := io.Copy(out, io.LimitReader(rc, MaxFileBytes+1))
	if err != nil {
		return n, err
	}
	if n > MaxFileBytes {
		return n, fmt.Errorf("larger than %d MB, whatever its header says", MaxFileBytes>>20)
	}
	return n, nil
}

// FileSHA256 hashes a file.
func FileSHA256(file string) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Package is an unpacked, validated plugin.
type Package struct {
	Dir      string
	Manifest Manifest
	SHA256   string
}

// ReadFile reads a file of the package, up to limit bytes. Nothing outside it can be reached.
func (p *Package) ReadFile(rel string, limit int64) ([]byte, error) {
	if err := checkPluginPath(rel); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(p.Dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("no file %q in the plugin", rel)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("no file %q in the plugin", rel)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("the file %q is too large", rel)
	}
	return data, nil
}

// ListFiles lists the files of the package under a folder, with forward slashes.
func (p *Package) ListFiles(dir string) ([]string, error) {
	if dir != "" {
		if err := checkPluginPath(dir); err != nil {
			return nil, err
		}
	}
	out := []string{}
	base := filepath.Join(p.Dir, filepath.FromSlash(dir))
	err := filepath.WalkDir(base, func(file string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			rel, err := filepath.Rel(p.Dir, file)
			if err != nil {
				return err
			}
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("no folder %q in the plugin", dir)
	}
	return out, nil
}
