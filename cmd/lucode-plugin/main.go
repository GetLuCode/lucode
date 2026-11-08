// lucode-plugin checks, packs and starts plugins. It runs the same validation the hub runs on an
// upload, from the same package, so the two cannot disagree.
package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"claude-orchestrator/internal/flows"
	"claude-orchestrator/internal/plugins"
)

const usage = `lucode-plugin checks, packs and starts lucode plugins.

  lucode-plugin new <id> [-dir path] [-plain]
                                        starts a plugin in a new folder (default: the last part of the
                                        id): a backend and a Preact page, or with -plain one JS file
                                        whose page is a view tree
  lucode-plugin build <dir>             compiles what plugin.json's "build" names: the backend for the
                                        plugin runtime, each page's frontend (JSX, TypeScript, CSS)
                                        for the browser
  lucode-plugin test <dir> [-run text]  builds, then runs test/**/*.test.{js,ts}: rpcs and flow nodes
                                        in the plugin runtime, pages in the browser's page runtime
  lucode-plugin validate <dir|zip>      everything the hub checks on an upload, and the blueprints
  lucode-plugin pack <dir> [-o out.zip] builds, validates, then writes the zip (default
                                        <id>-<version>.zip)

Names starting with . or _, *.d.ts files, node_modules, test/, the build's sources and the files of
a JS toolchain (package.json, tsconfig.json, lockfiles) stay out of the zip.
`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "lucode-plugin:", plugins.Scrub(err.Error()))
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(out, usage)
		return errors.New("name a command")
	}
	switch args[0] {
	case "validate":
		return cmdValidate(args[1:], out)
	case "pack":
		return cmdPack(args[1:], out)
	case "new":
		return cmdNew(args[1:], out)
	case "build":
		return cmdBuild(args[1:], out)
	case "test":
		return cmdTest(args[1:], out)
	case "help", "-h", "--help":
		fmt.Fprint(out, usage)
		return nil
	}
	return fmt.Errorf("unknown command %q: use new, build, test, validate or pack", args[0])
}

// parse reads one positional argument, with the flags before or after it.
func parse(fset *flag.FlagSet, args []string) (string, error) {
	fset.SetOutput(io.Discard)
	if err := fset.Parse(args); err != nil {
		return "", err
	}
	if fset.NArg() == 0 {
		return "", fmt.Errorf("%s needs an argument", fset.Name())
	}
	pos := fset.Arg(0)
	if err := fset.Parse(fset.Args()[1:]); err != nil {
		return "", err
	}
	if fset.NArg() > 0 {
		return "", fmt.Errorf("%s takes one argument, and %q is extra", fset.Name(), fset.Arg(0))
	}
	return pos, nil
}

func cmdValidate(args []string, out io.Writer) error {
	fset := flag.NewFlagSet("validate", flag.ContinueOnError)
	bundled := fset.Bool("bundled", false, "allow a lucode.* id, as a plugin that ships with lucode")
	target, err := parse(fset, args)
	if err != nil {
		return err
	}
	zipFile := target
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("%s cannot be read", target)
	}
	if info.IsDir() {
		tmp, err := os.MkdirTemp("", "lucode-plugin-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		zipFile = filepath.Join(tmp, "plugin.zip")
		if err := writeZip(target, zipFile, skipped(target)); err != nil {
			return err
		}
	}
	m, err := check(zipFile, *bundled)
	if err != nil {
		if info.IsDir() && m.Build != nil && strings.Contains(err.Error(), "which is not in the zip") {
			return fmt.Errorf("%w (run lucode-plugin build %s first)", err, target)
		}
		return err
	}
	report(out, m, "valid")
	return nil
}

func cmdPack(args []string, out io.Writer) error {
	fset := flag.NewFlagSet("pack", flag.ContinueOnError)
	output := fset.String("o", "", "the zip to write")
	bundled := fset.Bool("bundled", false, "allow a lucode.* id, as a plugin that ships with lucode")
	dir, err := parse(fset, args)
	if err != nil {
		return err
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("%s is not a folder", dir)
	}
	tmp, err := os.MkdirTemp("", "lucode-plugin-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := runBuild(dir, out); err != nil {
		return err
	}
	built := filepath.Join(tmp, "plugin.zip")
	if err := writeZip(dir, built, skipped(dir)); err != nil {
		return err
	}
	m, err := check(built, *bundled)
	if err != nil {
		return err
	}
	dest := *output
	if dest == "" {
		dest = m.ID + "-" + m.Version + ".zip"
	}
	if err := copyFile(built, dest); err != nil {
		return err
	}
	report(out, m, "packed to "+dest)
	return nil
}

func report(out io.Writer, m plugins.Manifest, what string) {
	fmt.Fprintf(out, "%s %s: %s\n", m.ID, m.Version, what)
	perms := plugins.PermissionsOf(m)
	if len(perms) == 0 {
		fmt.Fprintln(out, "  asks for no permissions")
		return
	}
	fmt.Fprintln(out, "  asks for:")
	for _, p := range perms {
		fmt.Fprintf(out, "    %-7s %-7s %s\n", p.Target, p.Risk, p.Label)
	}
}

// writeZip packs a folder the way the hub expects: forward slashes, plugin.json at the root. What
// stays out is what go:embed also leaves out of a bundled plugin, so both ship the same files.
func writeZip(dir, dest string, skip map[string]bool) error {
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		name := d.Name()
		if rel, err := filepath.Rel(dir, p); err == nil && skip[filepath.ToSlash(rel)] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a link, and a plugin cannot hold links", p)
		}
		if d.IsDir() || strings.HasSuffix(name, ".d.ts") {
			return nil
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(files)
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	w := zip.NewWriter(f)
	for _, p := range files {
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		e, err := w.CreateHeader(&zip.FileHeader{Name: filepath.ToSlash(rel), Method: zip.Deflate})
		if err != nil {
			return err
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(e, src)
		src.Close()
		if err != nil {
			return err
		}
	}
	if err := w.Close(); err != nil {
		return err
	}
	return f.Close()
}

func copyFile(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	return dst.Close()
}

// check runs what the hub runs on a zip, then what would otherwise only show up after installing:
// code that does not compile and blueprints that do not validate.
func check(zipFile string, bundled bool) (plugins.Manifest, error) {
	m, err := plugins.CheckZip(zipFile, plugins.ParseOptions{Bundled: bundled})
	if err != nil {
		return m, err
	}
	tmp, err := os.MkdirTemp("", "lucode-plugin-")
	if err != nil {
		return m, err
	}
	defer os.RemoveAll(tmp)
	dir := filepath.Join(tmp, "x")
	if err := plugins.ExtractZip(zipFile, dir); err != nil {
		return m, err
	}
	pkg := &plugins.Package{Dir: dir, Manifest: m}
	for _, target := range []string{plugins.TargetServer, plugins.TargetRunner} {
		if m.Target(target) == nil {
			continue
		}
		if err := checkCode(pkg, target); err != nil {
			return m, err
		}
	}
	return m, checkBlueprints(pkg)
}

// checkCode loads an entry file in the sandbox and asks for something it does not export, which
// compiles it and whatever it requires at its top level without calling any of its handlers. The
// host's calls are not there, so a top level that uses one is left for the hub to judge: only code
// that does not compile counts here.
func checkCode(pkg *plugins.Package, target string) error {
	rt := plugins.NewRuntime(plugins.Options{Package: pkg, Target: target})
	defer rt.Close()
	_, err := rt.Invoke(context.Background(), plugins.Invocation{Path: []string{"lucode-plugin validate"}, Timeout: 10 * time.Second})
	if err == nil || errors.Is(err, plugins.ErrNoHandler) {
		return nil
	}
	if strings.Contains(err.Error(), "does not compile") || strings.Contains(err.Error(), "only files inside the plugin") ||
		strings.Contains(err.Error(), "no such file") || strings.Contains(err.Error(), "only .js and .json") {
		return fmt.Errorf("%s target: %w", target, err)
	}
	return nil
}

// manifestNodes shows a package's own nodes to the flow validator, the way the hub's source does.
type manifestNodes struct{ m plugins.Manifest }

func (s manifestNodes) NodeTypes() []flows.NodeType {
	out := []flows.NodeType{}
	for _, n := range s.m.Contributes.FlowNodes {
		fields := make([]flows.Field, 0, len(n.Fields))
		for _, f := range n.Fields {
			fields = append(fields, flows.Field{
				Name: f.Name, Label: f.Label, Kind: f.Kind, Required: f.Required, Options: f.Options,
				Default: f.Default, Help: f.Help, Min: f.Min, PortLabel: f.PortLabel,
			})
		}
		out = append(out, flows.NodeType{
			Type: "plugin." + s.m.ID + "." + n.ID, Title: n.Label, Category: flows.CategoryAction,
			Description: n.Description, Ports: n.Ports, Fields: fields, PluginID: s.m.ID, PluginName: s.m.Name,
		})
	}
	return out
}
func (s manifestNodes) Blueprints() []flows.Blueprint  { return nil }
func (s manifestNodes) Installed(pluginID string) bool { return pluginID == s.m.ID }
func (s manifestNodes) RunNode(context.Context, flows.PluginCall) (string, map[string]any, error) {
	return "", nil, errors.New("nothing runs here")
}

func checkBlueprints(pkg *plugins.Package) error {
	flows.SetPluginSource(manifestNodes{pkg.Manifest})
	defer flows.SetPluginSource(nil)
	for _, b := range pkg.Manifest.Contributes.Blueprints {
		raw, err := pkg.ReadFile(b.File, plugins.MaxFileBytes)
		if err != nil {
			return fmt.Errorf("blueprint %s: %w", b.Key, err)
		}
		var probe struct {
			Nodes json.RawMessage `json:"nodes"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil || len(probe.Nodes) == 0 {
			return fmt.Errorf("blueprint %s: %s is not a flow graph", b.Key, path.Clean(b.File))
		}
		g, err := flows.ParseGraph(raw)
		if err != nil {
			return fmt.Errorf("blueprint %s: %w", b.Key, err)
		}
		if problems := flows.Validate(g); len(problems) > 0 {
			lines := make([]string, 0, len(problems))
			for _, p := range problems {
				lines = append(lines, p.Message)
			}
			return fmt.Errorf("blueprint %s cannot be published: %s", b.Key, strings.Join(lines, "; "))
		}
	}
	return nil
}

func cmdNew(args []string, out io.Writer) error {
	fset := flag.NewFlagSet("new", flag.ContinueOnError)
	dirFlag := fset.String("dir", "", "the folder to create")
	plain := fset.Bool("plain", false, "one JS file whose page is a view tree, with nothing to build")
	id, err := parse(fset, args)
	if err != nil {
		return err
	}
	dir := *dirFlag
	if dir == "" {
		dir = id[strings.LastIndex(id, ".")+1:]
	}
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("%s already exists", dir)
	}
	short := id[strings.LastIndex(id, ".")+1:]
	name := strings.ToUpper(short[:min(1, len(short))]) + short[min(1, len(short)):]
	fill := strings.NewReplacer("__ID__", id, "__NAME__", name)
	files := map[string]string{
		"plugin.json":     newManifest,
		"main.js":         newMain,
		"assets/icon.svg": newIcon,
		"README.md":       newReadme,
	}
	if !*plain {
		files = frontendTemplate()
	}
	for rel, body := range files {
		if strings.HasSuffix(rel, ".d.ts") {
			continue // the SDK's own text, which may hold the placeholders' letters
		}
		files[rel] = fill.Replace(body)
	}
	// the same parser as the hub: a bad id or a reserved one stops here, before anything is written
	if _, err := plugins.ParseManifest([]byte(files["plugin.json"]), plugins.ParseOptions{}); err != nil {
		return err
	}
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return err
		}
	}
	next := "validate"
	if !*plain {
		next = "test"
	}
	fmt.Fprintf(out, "%s is ready in %s\n  lucode-plugin %s %s\n  lucode-plugin pack %s\n", id, dir, next, dir, dir)
	return nil
}

const newManifest = `{
  "id": "__ID__",
  "name": "__NAME__",
  "version": "0.1.0",
  "sdk": 1,
  "description": "What it does, in one sentence.",
  "icon": "assets/icon.svg",
  "targets": {
    "server": { "main": "main.js", "permissions": {} }
  },
  "contributes": {
    "pages": [
      { "id": "main", "label": "__NAME__", "icon": "assets/icon.svg", "render": "renderMain" }
    ]
  }
}
`

const newMain = `// Runs on the hub. A page is an rpc that returns a view tree: JSON the host draws.
"use strict";

function renderMain() {
  return {
    type: "stack",
    gap: 16,
    children: [
      { type: "heading", text: "Hello from " + lucode.plugin.name, level: 1 },
      { type: "text", tone: "muted", text: "Version " + lucode.plugin.version },
    ],
  };
}

module.exports = {
  rpc: { renderMain },
};
`

const newIcon = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 48" fill="none">
  <rect x="3" y="3" width="42" height="42" rx="11" fill="#17171b" stroke="#2e2e35" stroke-width="2"/>
  <circle cx="24" cy="24" r="8" stroke="#00d4aa" stroke-width="4"/>
</svg>
`

const newReadme = `# __NAME__

Install it from Settings, Plugins: run ` + "`lucode-plugin pack .`" + ` and drop the zip there.

How plugins work: docs/plugin-sdk.md in the lucode repository.
`
