// Package compile compiles a plugin's sources for lucode-plugin: the backend (targets.server and
// targets.runner) to one CommonJS file the plugin runtime loads, and each frontend page to one
// script the browser runs in a Worker, with Preact from this package. Only the CLI imports it:
// the hub and the runner load what a zip already holds, and never link the compiler.
package compile

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/evanw/esbuild/pkg/api"

	"claude-orchestrator/internal/plugins"
)

//go:embed vendor/preact/*.mjs
var vendorFS embed.FS

// PreactVersion is the Preact every frontend page is compiled with.
const PreactVersion = "11.2.0"

// Output is one file a build wrote.
type Output struct {
	What   string // "server", "runner", "page main", "page main (style)"
	Source string
	File   string
	Bytes  int
}

// Result is what a build wrote, and what esbuild warned about.
type Result struct {
	Outputs  []Output
	Warnings []string
}

// ReadManifest reads plugin.json from a plugin's folder, checked as the hub checks it. A lucode.*
// id is accepted: building is not installing.
func ReadManifest(dir string) (plugins.Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return plugins.Manifest{}, fmt.Errorf("%s has no plugin.json", dir)
	}
	return plugins.ParseManifest(raw, plugins.ParseOptions{Bundled: true})
}

// Build compiles what plugin.json's "build" names. Without one there is nothing to do.
func Build(dir string) (Result, error) {
	var res Result
	m, err := ReadManifest(dir)
	if err != nil {
		return res, err
	}
	if m.Build == nil {
		return res, nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return res, err
	}
	b := m.Build
	if b.Server != "" {
		if err := backend(abs, "server", b.Server, m.Targets.Server.Main, &res); err != nil {
			return res, err
		}
	}
	if b.Runner != "" {
		if err := backend(abs, "runner", b.Runner, m.Targets.Runner.Main, &res); err != nil {
			return res, err
		}
	}
	ids := make([]string, 0, len(b.Pages))
	for id := range b.Pages {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		for _, p := range m.Contributes.Pages {
			if p.ID == id {
				if err := frontend(abs, p, b.Pages[id], &res); err != nil {
					return res, err
				}
			}
		}
	}
	return res, nil
}

// Sources are the files and folders a build reads from, which do not belong in the zip: every
// top-level folder that holds a source, unless an output is in it too.
func Sources(m plugins.Manifest) []string {
	if m.Build == nil {
		return nil
	}
	srcs := []string{m.Build.Server, m.Build.Runner}
	for _, s := range m.Build.Pages {
		srcs = append(srcs, s)
	}
	outs := map[string]bool{}
	for _, t := range []*plugins.Target{m.Targets.Server, m.Targets.Runner} {
		if t != nil {
			outs[top(t.Main)] = true
		}
	}
	for _, p := range m.Contributes.Pages {
		if p.UI != "" {
			outs[top(p.UI)] = true
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range srcs {
		if s == "" {
			continue
		}
		t := top(s)
		if !strings.Contains(s, "/") {
			t = s // a source at the root: just that file
		} else if outs[t] {
			t = s
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

func top(p string) string {
	first, _, _ := strings.Cut(p, "/")
	return first
}

var commonLoaders = map[string]api.Loader{
	".json": api.LoaderJSON,
	".txt":  api.LoaderText,
	".md":   api.LoaderText,
}

// backend: one CommonJS file for the plugin runtime, which has neither Node's modules nor a
// module loader beyond its own require, so everything goes in. ES2017 keeps out syntax the
// runtime does not parse.
func backend(dir, what, src, out string, res *Result) error {
	r := api.Build(api.BuildOptions{
		AbsWorkingDir: dir,
		EntryPoints:   []string{filepath.Join(dir, filepath.FromSlash(src))},
		Outfile:       filepath.Join(dir, filepath.FromSlash(out)),
		Bundle:        true,
		Write:         false,
		Format:        api.FormatCommonJS,
		Platform:      api.PlatformNeutral,
		MainFields:    []string{"module", "main"},
		Target:        api.ES2017,
		Charset:       api.CharsetUTF8,
		LegalComments: api.LegalCommentsEndOfFile,
		Loader:        commonLoaders,
		Define:        map[string]string{"process.env.NODE_ENV": `"production"`},
		Banner:        map[string]string{"js": "// Built by lucode-plugin from " + src + ". Edit the source, not this file."},
		LogLevel:      api.LogLevelSilent,
	})
	if err := messages(what, r); err != nil {
		return err
	}
	warn(what, r, res)
	return write(dir, what, src, r, res)
}

// frontend: one script for the page's Worker, JSX through Preact's runtime, images and fonts
// inlined (the Worker and its page load nothing else), and the page's CSS beside it.
func frontend(dir string, page plugins.PageDef, src string, res *Result) error {
	loaders := map[string]api.Loader{}
	for k, v := range commonLoaders {
		loaders[k] = v
	}
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".woff", ".woff2", ".ttf", ".otf"} {
		loaders[ext] = api.LoaderDataURL
	}
	what := "page " + page.ID
	r := api.Build(api.BuildOptions{
		AbsWorkingDir:   dir,
		EntryPoints:     []string{filepath.Join(dir, filepath.FromSlash(src))},
		Outfile:         filepath.Join(dir, filepath.FromSlash(page.UI)),
		Bundle:          true,
		Write:           false,
		Format:          api.FormatIIFE,
		Platform:        api.PlatformBrowser,
		Target:          api.ES2020,
		Charset:         api.CharsetUTF8,
		LegalComments:   api.LegalCommentsEndOfFile,
		JSX:             api.JSXAutomatic,
		JSXImportSource: "preact",
		Loader:          loaders,
		Define:          map[string]string{"process.env.NODE_ENV": `"production"`},
		Plugins:         []api.Plugin{preactPlugin},
		Banner:          map[string]string{"js": "// Built by lucode-plugin from " + src + ". Edit the source, not this file."},
		LogLevel:        api.LogLevelSilent,
	})
	if err := messages(what, r); err != nil {
		return err
	}
	warn(what, r, res)
	// a stylesheet left from an earlier build that no longer has one would still be served
	style := filepath.Join(dir, filepath.FromSlash(page.Style()))
	hasStyle := false
	for _, f := range r.OutputFiles {
		if filepath.Clean(f.Path) == filepath.Clean(style) {
			hasStyle = true
		}
	}
	if !hasStyle {
		if err := os.Remove(style); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return write(dir, what, src, r, res)
}

// uiModule is "@lucode/ui": the bridge the Worker runtime puts in place before the page's script.
const uiModule = `const l = globalThis.lucode;
if (!l) throw new Error("@lucode/ui only works in a plugin page");
export const context = l.context;
export const rpc = l.rpc;
export const toast = l.toast;
export const openLink = l.openLink;
export const asset = l.asset;
export const on = l.on;
`

var preactFiles = map[string]string{
	"preact":                 "vendor/preact/preact.mjs",
	"preact/hooks":           "vendor/preact/hooks.mjs",
	"preact/jsx-runtime":     "vendor/preact/jsx-runtime.mjs",
	"preact/jsx-dev-runtime": "vendor/preact/jsx-runtime.mjs",
}

// preactPlugin gives every import of preact the copy in this package, whatever node_modules has:
// a page is always built against the Preact its host was tested with.
var preactPlugin = api.Plugin{
	Name: "lucode-preact",
	Setup: func(b api.PluginBuild) {
		b.OnResolve(api.OnResolveOptions{Filter: `^@lucode/ui$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			return api.OnResolveResult{Path: "ui", Namespace: "lucode-ui"}, nil
		})
		b.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "lucode-ui"}, func(api.OnLoadArgs) (api.OnLoadResult, error) {
			text := uiModule
			return api.OnLoadResult{Contents: &text, Loader: api.LoaderJS}, nil
		})
		b.OnResolve(api.OnResolveOptions{Filter: `^preact(/.*)?$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			if _, ok := preactFiles[a.Path]; !ok {
				return api.OnResolveResult{}, fmt.Errorf("%s is not available: a page has preact, preact/hooks and preact/jsx-runtime", a.Path)
			}
			return api.OnResolveResult{Path: a.Path, Namespace: "lucode-preact"}, nil
		})
		b.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "lucode-preact"}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			raw, err := vendorFS.ReadFile(preactFiles[a.Path])
			if err != nil {
				return api.OnLoadResult{}, err
			}
			text := string(raw)
			return api.OnLoadResult{Contents: &text, Loader: api.LoaderJS}, nil
		})
	},
}

func messages(what string, r api.BuildResult) error {
	if len(r.Errors) == 0 {
		return nil
	}
	lines := make([]string, 0, len(r.Errors))
	for i, e := range r.Errors {
		if i == 10 {
			lines = append(lines, fmt.Sprintf("  ... and %d more", len(r.Errors)-10))
			break
		}
		lines = append(lines, "  "+describe(e))
	}
	return fmt.Errorf("%s does not build:\n%s", what, strings.Join(lines, "\n"))
}

func warn(what string, r api.BuildResult, res *Result) {
	for _, w := range r.Warnings {
		res.Warnings = append(res.Warnings, what+": "+describe(w))
	}
}

func describe(m api.Message) string {
	if m.Location == nil {
		return m.Text
	}
	return fmt.Sprintf("%s:%d:%d: %s", filepath.ToSlash(m.Location.File), m.Location.Line, m.Location.Column+1, m.Text)
}

func write(dir, what, src string, r api.BuildResult, res *Result) error {
	for _, f := range r.OutputFiles {
		rel, err := filepath.Rel(dir, f.Path)
		if err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("%s: esbuild wanted to write %s, outside the plugin", what, f.Path)
		}
		if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(f.Path, f.Contents, 0o644); err != nil {
			return err
		}
		label := what
		if filepath.Ext(f.Path) == ".css" {
			label += " (style)"
		}
		res.Outputs = append(res.Outputs, Output{What: label, Source: src, File: filepath.ToSlash(rel), Bytes: len(f.Contents)})
	}
	return nil
}
