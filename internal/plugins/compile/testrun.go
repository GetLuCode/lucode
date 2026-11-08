package compile

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/evanw/esbuild/pkg/api"

	"claude-orchestrator/internal/plugins"
	"claude-orchestrator/internal/plugins/surface"
)

//go:embed testkit.js
var testkitJS string

//go:embed testpage.js
var testpageJS string

// TestProject is the project id a test runs in, unless it names another.
const TestProject = "test-project"

// TestSummary counts what RunTests ran.
type TestSummary struct{ Passed, Failed, Skipped int }

// TestFiles are the test files of a plugin: test/**/*.test.{js,ts,jsx,tsx}.
func TestFiles(dir string) ([]string, error) {
	var out []string
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(abs, "test")
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return filepath.SkipAll
			}
			return err
		}
		name := d.Name()
		if d.IsDir() && name == "node_modules" {
			return filepath.SkipDir
		}
		for _, ext := range []string{".test.js", ".test.ts", ".test.jsx", ".test.tsx"} {
			if !d.IsDir() && strings.HasSuffix(name, ext) {
				out = append(out, p)
			}
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// RunTests runs a built plugin's tests: its backend in the same runtime as the hub's, with
// storage, settings and flows.emit in memory, and its frontend pages in the same Worker runtime
// as the browser's, on a virtual clock. filter keeps the tests whose name contains it.
func RunTests(dir, filter string, out io.Writer) (TestSummary, error) {
	var sum TestSummary
	m, err := ReadManifest(dir)
	if err != nil {
		return sum, err
	}
	files, err := TestFiles(dir)
	if err != nil {
		return sum, err
	}
	if len(files) == 0 {
		return sum, errors.New("there are no tests: add files named test/<something>.test.ts (or .js)")
	}
	abs, _ := filepath.Abs(dir)
	pkg := &plugins.Package{Dir: abs, Manifest: m}
	for _, f := range files {
		rel, _ := filepath.Rel(abs, f)
		rel = filepath.ToSlash(rel)
		code, err := compileTest(abs, f)
		if err != nil {
			fmt.Fprintf(out, "FAIL %s\n  %s\n", rel, indent(err.Error()))
			sum.Failed++
			continue
		}
		s := runFile(pkg, rel, code, filter, out)
		sum.Passed += s.Passed
		sum.Failed += s.Failed
		sum.Skipped += s.Skipped
	}
	return sum, nil
}

func compileTest(dir, file string) (string, error) {
	r := api.Build(api.BuildOptions{
		AbsWorkingDir: dir,
		EntryPoints:   []string{file},
		Bundle:        true,
		Write:         false,
		Format:        api.FormatCommonJS,
		Platform:      api.PlatformNeutral,
		MainFields:    []string{"module", "main"},
		Target:        api.ES2017,
		Loader:        commonLoaders,
		Outdir:        filepath.Join(dir, ".lucode-test"),
		LogLevel:      api.LogLevelSilent,
	})
	if err := messages(filepath.Base(file), r); err != nil {
		return "", err
	}
	for _, f := range r.OutputFiles {
		if strings.HasSuffix(f.Path, ".js") {
			return string(f.Contents), nil
		}
	}
	return "", errors.New("esbuild wrote nothing")
}

func indent(s string) string { return strings.ReplaceAll(s, "\n", "\n  ") }

// ---- one file ------------------------------------------------------------------------------

type tester struct {
	pkg      *plugins.Package
	rt       *plugins.Runtime
	vm       *goja.Runtime
	out      io.Writer
	store    map[string]string // storage, as JSON
	settings map[string]any
	events   []map[string]any
}

func runFile(pkg *plugins.Package, rel, code, filter string, out io.Writer) TestSummary {
	var sum TestSummary
	t := &tester{pkg: pkg, out: out}
	if pkg.Manifest.Targets.Server != nil {
		t.rt = plugins.NewRuntime(plugins.Options{Package: pkg, Target: plugins.TargetServer})
		defer t.rt.Close()
	}
	vm := goja.New()
	t.vm = vm
	t.reset()
	installConsole(vm, out, "  ")
	if _, err := vm.RunString(testkitJS); err != nil {
		fmt.Fprintf(out, "FAIL %s\n  %v\n", rel, err)
		sum.Failed++
		return sum
	}
	_ = vm.Set("plugin", t.pluginObject())
	module := vm.NewObject()
	_ = module.Set("exports", vm.NewObject())
	fn, err := vm.RunScript(rel, "(function(module,exports){"+code+"\n})")
	if err == nil {
		call, _ := goja.AssertFunction(fn)
		_, err = call(goja.Undefined(), module, module.Get("exports"))
	}
	if err != nil {
		fmt.Fprintf(out, "FAIL %s\n  %s\n", rel, indent(errText(err)))
		sum.Failed++
		return sum
	}
	list := vm.Get("__lucodeTests").Export().([]any)
	only := false
	for _, e := range list {
		if e.(map[string]any)["only"] == true {
			only = true
		}
	}
	fmt.Fprintf(out, "%s\n", rel)
	for i := range list {
		e := vm.Get("__lucodeTests").ToObject(vm).Get(fmt.Sprint(i)).ToObject(vm)
		name := e.Get("name").String()
		if (only && !truthy(e, "only")) || (filter != "" && !strings.Contains(name, filter)) {
			continue
		}
		if truthy(e, "skip") {
			fmt.Fprintf(out, "  skip  %s\n", name)
			sum.Skipped++
			continue
		}
		t.reset()
		start := time.Now()
		err := t.runOne(e.Get("fn"))
		took := time.Since(start).Round(time.Millisecond)
		if err != nil {
			fmt.Fprintf(out, "  FAIL  %s (%s)\n        %s\n", name, took, strings.ReplaceAll(errText(err), "\n", "\n        "))
			sum.Failed++
			continue
		}
		fmt.Fprintf(out, "  ok    %s (%s)\n", name, took)
		sum.Passed++
	}
	return sum
}

func truthy(o *goja.Object, key string) bool {
	v := o.Get(key)
	return v != nil && v.ToBoolean()
}

func (t *tester) reset() {
	t.store = map[string]string{}
	t.settings = map[string]any{}
	t.events = nil
}

func (t *tester) runOne(fn goja.Value) (err error) {
	call, ok := goja.AssertFunction(fn)
	if !ok {
		return errors.New("test() needs a function")
	}
	v, err := call(goja.Undefined())
	if err != nil {
		return err
	}
	if p, ok := v.Export().(*goja.Promise); ok {
		switch p.State() {
		case goja.PromiseStateRejected:
			return jsError(t.vm, p.Result())
		case goja.PromiseStatePending:
			return errors.New("the test is still waiting for something: page actions and plugin.rpc finish before they return, so a test needs no timers")
		}
	}
	return nil
}

func jsError(vm *goja.Runtime, v goja.Value) error {
	if o, ok := v.(*goja.Object); ok {
		if msg := o.Get("message"); msg != nil && !goja.IsUndefined(msg) {
			return errors.New(msg.String())
		}
	}
	return errors.New(v.String())
}

func errText(err error) string {
	var ex *goja.Exception
	if errors.As(err, &ex) {
		if o, ok := ex.Value().(*goja.Object); ok {
			if msg := o.Get("message"); msg != nil && !goja.IsUndefined(msg) {
				return msg.String() + where(ex)
			}
		}
		return ex.Value().String() + where(ex)
	}
	return err.Error()
}

func where(ex *goja.Exception) string {
	stack := ex.String()
	for _, line := range strings.Split(stack, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "at ") && !strings.Contains(line, "native") {
			return "\n" + line
		}
	}
	return ""
}

func installConsole(vm *goja.Runtime, out io.Writer, prefix string) {
	console := vm.NewObject()
	for _, level := range []string{"log", "info", "warn", "error", "debug"} {
		level := level
		_ = console.Set(level, func(c goja.FunctionCall) goja.Value {
			parts := make([]string, 0, len(c.Arguments))
			for _, a := range c.Arguments {
				if a.ExportType() != nil && a.ExportType().Kind().String() == "string" {
					parts = append(parts, a.String())
					continue
				}
				raw, err := json.Marshal(a.Export())
				if err != nil {
					parts = append(parts, a.String())
				} else {
					parts = append(parts, string(raw))
				}
			}
			tag := ""
			if level == "warn" || level == "error" {
				tag = level + ": "
			}
			fmt.Fprintf(out, "%s%s%s\n", prefix, tag, strings.Join(parts, " "))
			return goja.Undefined()
		})
	}
	_ = vm.Set("console", console)
}

// js turns a JSON-plain Go value into a plain value of vm (arrays are arrays, objects objects).
func js(vm *goja.Runtime, v any) goja.Value {
	raw, err := json.Marshal(v)
	if err != nil {
		return goja.Undefined()
	}
	parse, _ := goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get("parse"))
	out, err := parse(goja.Undefined(), vm.ToValue(string(raw)))
	if err != nil {
		return goja.Undefined()
	}
	return out
}

// plain is a vm value as JSON-plain Go.
func plain(vm *goja.Runtime, v goja.Value) (any, error) {
	if v == nil || goja.IsUndefined(v) {
		return nil, nil
	}
	stringify, _ := goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get("stringify"))
	s, err := stringify(goja.Undefined(), v)
	if err != nil {
		return nil, err
	}
	if goja.IsUndefined(s) {
		return nil, nil
	}
	var out any
	err = json.Unmarshal([]byte(s.String()), &out)
	return out, err
}

func throw(vm *goja.Runtime, format string, args ...any) {
	panic(vm.NewGoError(fmt.Errorf(format, args...)))
}

// ---- the plugin global ---------------------------------------------------------------------

func (t *tester) pluginObject() *goja.Object {
	vm := t.vm
	o := vm.NewObject()
	m := t.pkg.Manifest
	_ = o.Set("id", m.ID)
	_ = o.Set("name", m.Name)
	_ = o.Set("version", m.Version)

	_ = o.Set("rpc", func(c goja.FunctionCall) goja.Value {
		name := c.Argument(0).String()
		args, err := plain(vm, c.Argument(1))
		if err != nil {
			throw(vm, "plugin.rpc: the arguments must be plain JSON")
		}
		opts, _ := plain(vm, c.Argument(2))
		v, err := t.rpc(name, args, optString(opts, "projectId", TestProject))
		if err != nil {
			throw(vm, "%s", err.Error())
		}
		return js(vm, v)
	})
	_ = o.Set("node", func(c goja.FunctionCall) goja.Value {
		id := c.Argument(0).String()
		in, _ := plain(vm, c.Argument(1))
		v, err := t.node(id, in)
		if err != nil {
			throw(vm, "%s", err.Error())
		}
		return js(vm, v)
	})

	storage := vm.NewObject()
	_ = storage.Set("get", func(c goja.FunctionCall) goja.Value {
		raw, ok := t.store[c.Argument(0).String()]
		if !ok {
			return goja.Null()
		}
		var v any
		_ = json.Unmarshal([]byte(raw), &v)
		return js(vm, v)
	})
	_ = storage.Set("set", func(c goja.FunctionCall) goja.Value {
		v, err := plain(vm, c.Argument(1))
		if err != nil {
			throw(vm, "plugin.storage.set: the value must be plain JSON")
		}
		raw, _ := json.Marshal(v)
		t.store[c.Argument(0).String()] = string(raw)
		return goja.Undefined()
	})
	_ = storage.Set("keys", func(goja.FunctionCall) goja.Value { return js(vm, t.storeKeys("")) })
	_ = o.Set("storage", storage)

	_ = o.Set("setSetting", func(c goja.FunctionCall) goja.Value {
		v, _ := plain(vm, c.Argument(1))
		t.settings[c.Argument(0).String()] = v
		return goja.Undefined()
	})
	_ = o.Set("events", func(goja.FunctionCall) goja.Value { return js(vm, t.events) })
	_ = o.Set("logs", func(goja.FunctionCall) goja.Value {
		if t.rt == nil {
			return js(vm, []any{})
		}
		return js(vm, t.rt.Logs())
	})
	_ = o.Set("page", func(c goja.FunctionCall) goja.Value {
		opts, _ := plain(vm, c.Argument(1))
		p, err := t.openPage(c.Argument(0).String(), optString(opts, "projectId", TestProject))
		if err != nil {
			throw(vm, "%s", err.Error())
		}
		return p.object()
	})
	return o
}

func optString(opts any, key, fallback string) string {
	if m, ok := opts.(map[string]any); ok {
		if s, ok := m[key].(string); ok {
			return s
		}
	}
	return fallback
}

func (t *tester) perms() plugins.Permissions {
	if s := t.pkg.Manifest.Targets.Server; s != nil {
		return s.Permissions // a test runs with everything the plugin asks for granted
	}
	return plugins.Permissions{}
}

func (t *tester) invoke(path []string, arg any, c plugins.Context) (any, error) {
	if t.rt == nil {
		return nil, errors.New("this plugin has no server target")
	}
	p := t.perms()
	return t.rt.Invoke(context.Background(), plugins.Invocation{
		Path: path, Arg: arg, Context: c, Perms: p, Modules: t.modules(p),
	})
}

// rpc calls the backend as the browser's road in does: the arguments, with the project's id.
func (t *tester) rpc(name string, args any, projectID string) (any, error) {
	merged := map[string]any{}
	if m, ok := args.(map[string]any); ok {
		for k, v := range m {
			merged[k] = v
		}
	} else if args != nil {
		merged["value"] = args
	}
	merged["projectId"] = projectID
	c := plugins.Context{ProjectID: projectID, Kind: "rpc"}
	c.SessionID, _ = merged["sessionId"].(string)
	c.CardID, _ = merged["cardId"].(string)
	return t.invoke([]string{"rpc", name}, merged, c)
}

// node runs a flow node as the engine does: {config, card, event, nodes, run, projectId}.
func (t *tester) node(id string, in any) (any, error) {
	found := false
	for _, n := range t.pkg.Manifest.Contributes.FlowNodes {
		if n.ID == id {
			found = true
			if n.Target == plugins.TargetRunner {
				return nil, fmt.Errorf("the node %q runs on a runner, which lucode-plugin test does not have", id)
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("plugin.json has no flow node %q", id)
	}
	arg := map[string]any{"config": map[string]any{}, "projectId": TestProject}
	if m, ok := in.(map[string]any); ok {
		for k, v := range m {
			arg[k] = v
		}
	}
	pid, _ := arg["projectId"].(string)
	c := plugins.Context{ProjectID: pid, Kind: "node"}
	if card, ok := arg["card"].(map[string]any); ok {
		c.CardID, _ = card["id"].(string)
	}
	return t.invoke([]string{"nodes", id}, arg, c)
}

func (t *tester) storeKeys(prefix string) []string {
	keys := []string{}
	for k := range t.store {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// modules are the hub's part of the SDK, in memory. What a test cannot have (the board, the
// browser, other runners) is left out, and the runtime says it is not available here.
func (t *tester) modules(p plugins.Permissions) []plugins.Module {
	need := func(s *plugins.Scope, ok bool, perm string) {
		if !ok {
			s.Throw("this needs the %s permission, which this plugin does not ask for in plugin.json", perm)
		}
	}
	setting := func(def plugins.SettingDef) any {
		if v, ok := t.settings[def.Key]; ok {
			return v
		}
		return def.Default
	}
	return []plugins.Module{
		{Name: "storage", Funcs: map[string]plugins.Func{
			"get": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
				need(s, p.Storage, "storage")
				raw, ok := t.store[s.Str(c, 0, "key")]
				if !ok {
					return goja.Null()
				}
				var v any
				_ = json.Unmarshal([]byte(raw), &v)
				return s.JS(v)
			},
			"set": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
				need(s, p.Storage, "storage")
				key := s.Str(c, 0, "key")
				raw, err := json.Marshal(s.Plain(c.Argument(1)))
				if err != nil {
					s.Throw("this value cannot be stored")
				}
				t.store[key] = string(raw)
				return goja.Undefined()
			},
			"delete": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
				need(s, p.Storage, "storage")
				delete(t.store, s.Str(c, 0, "key"))
				return goja.Undefined()
			},
			"list": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
				need(s, p.Storage, "storage")
				prefix := ""
				if a := c.Argument(0); !goja.IsUndefined(a) && !goja.IsNull(a) {
					prefix = a.String()
				}
				return s.JS(t.storeKeys(prefix))
			},
		}},
		{Name: "settings", Funcs: map[string]plugins.Func{
			"get": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
				key := s.Str(c, 0, "key")
				for _, def := range t.pkg.Manifest.Contributes.Settings {
					if def.Key == key {
						return s.JS(setting(def))
					}
				}
				return goja.Undefined()
			},
			"all": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
				out := map[string]any{}
				for _, def := range t.pkg.Manifest.Contributes.Settings {
					out[def.Key] = setting(def)
				}
				return s.JS(out)
			},
		}},
		{Name: "flows", Funcs: map[string]plugins.Func{
			"emit": func(s *plugins.Scope, c goja.FunctionCall) goja.Value {
				need(s, p.Flows, "flows")
				payload, _ := s.Plain(c.Argument(1)).(map[string]any)
				t.events = append(t.events, map[string]any{"event": s.Str(c, 0, "event"), "payload": payload})
				return goja.Undefined()
			},
		}},
	}
}

// ---- a page under test ---------------------------------------------------------------------

type timer struct {
	id       int64
	at       float64
	every    float64 // 0: once
	fn       goja.Callable
	args     []goja.Value
	canceled bool
}

type page struct {
	t        *tester
	def      plugins.PageDef
	vm       *goja.Runtime
	helper   *goja.Object
	listen   map[string][]goja.Callable
	outbox   []map[string]any
	timers   []*timer
	timerSeq int64
	now      float64
	evSeq    int
	prevent  map[int]bool
	errs     []string
	seenErrs int
	toasts   []map[string]any
	opened   []string
	calls    []map[string]any
	ready    bool
	rejected []*goja.Promise // rejected with no handler yet
}

func (t *tester) openPage(id, projectID string) (*page, error) {
	var def plugins.PageDef
	for _, d := range t.pkg.Manifest.Contributes.Pages {
		if d.ID == id {
			def = d
		}
	}
	if def.ID == "" {
		return nil, fmt.Errorf("plugin.json has no page %q", id)
	}
	if def.UI == "" {
		return nil, fmt.Errorf(`the page %q draws view trees: test its "render" rpc with plugin.rpc`, id)
	}
	bundle, err := t.pkg.ReadFile(def.UI, plugins.MaxCodeBytes)
	if err != nil {
		return nil, fmt.Errorf("%v: run lucode-plugin build first", err)
	}
	m := t.pkg.Manifest
	p := &page{t: t, def: def, vm: goja.New(), listen: map[string][]goja.Callable{}, prevent: map[int]bool{}, now: 0}
	p.install()
	script := surface.Script(surface.Context{PluginID: m.ID, PluginName: m.Name, PluginVersion: m.Version, PageID: id, ProjectID: projectID}, bundle)
	if _, err := p.vm.RunScript(def.UI, string(script)); err != nil {
		return nil, fmt.Errorf("the page's script threw while loading: %s", errText(err))
	}
	if _, err := p.vm.RunString(testpageJS); err != nil {
		return nil, err
	}
	p.helper = p.vm.Get("__lucodeTest").ToObject(p.vm)
	if err := p.settle(1000); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *page) install() {
	vm := p.vm
	g := vm.GlobalObject()
	_ = vm.Set("self", g)
	installConsole(vm, p.t.out, "  page: ")
	_ = vm.Set("postMessage", func(c goja.FunctionCall) goja.Value {
		o, ok := c.Argument(0).(*goja.Object)
		if !ok {
			return goja.Undefined()
		}
		if t := o.Get("t"); t != nil && t.String() == "ops" {
			return goja.Undefined() // the document here is the page's own: nothing to draw
		}
		v, err := plain(vm, o)
		if err != nil {
			throw(vm, "DataCloneError: a message of the page could not be sent")
		}
		if m, ok := v.(map[string]any); ok {
			p.outbox = append(p.outbox, m)
		}
		return goja.Undefined()
	})
	_ = vm.Set("addEventListener", func(c goja.FunctionCall) goja.Value {
		if fn, ok := goja.AssertFunction(c.Argument(1)); ok {
			name := c.Argument(0).String()
			p.listen[name] = append(p.listen[name], fn)
		}
		return goja.Undefined()
	})
	_ = vm.Set("removeEventListener", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
	if _, err := vm.RunString(`self.queueMicrotask = function (fn) { Promise.resolve().then(function () { fn(); }); };`); err != nil {
		panic(err)
	}
	// a rejection nobody handles is what the browser reports as unhandledrejection
	vm.SetPromiseRejectionTracker(func(pr *goja.Promise, op goja.PromiseRejectionOperation) {
		if op == goja.PromiseRejectionReject {
			p.rejected = append(p.rejected, pr)
		} else {
			for i, r := range p.rejected {
				if r == pr {
					p.rejected = append(p.rejected[:i], p.rejected[i+1:]...)
					break
				}
			}
		}
	})
	addTimer := func(c goja.FunctionCall, repeat bool) goja.Value {
		fn, ok := goja.AssertFunction(c.Argument(0))
		if !ok {
			return vm.ToValue(0)
		}
		delay := c.Argument(1).ToFloat()
		if delay < 0 || delay != delay {
			delay = 0
		}
		p.timerSeq++
		tm := &timer{id: p.timerSeq, at: p.now + delay, fn: fn}
		if repeat {
			tm.every = max(delay, 1)
		}
		if len(c.Arguments) > 2 {
			tm.args = c.Arguments[2:]
		}
		p.timers = append(p.timers, tm)
		return vm.ToValue(tm.id)
	}
	cancel := func(c goja.FunctionCall) goja.Value {
		id := c.Argument(0).ToInteger()
		for _, tm := range p.timers {
			if tm.id == id {
				tm.canceled = true
			}
		}
		return goja.Undefined()
	}
	_ = vm.Set("setTimeout", func(c goja.FunctionCall) goja.Value { return addTimer(c, false) })
	_ = vm.Set("setInterval", func(c goja.FunctionCall) goja.Value { return addTimer(c, true) })
	_ = vm.Set("clearTimeout", cancel)
	_ = vm.Set("clearInterval", cancel)
	_ = vm.Set("requestAnimationFrame", func(c goja.FunctionCall) goja.Value {
		fn, ok := goja.AssertFunction(c.Argument(0))
		if !ok {
			return vm.ToValue(0)
		}
		p.timerSeq++
		frame := p.timerSeq
		p.timers = append(p.timers, &timer{id: frame, at: p.now + 16, fn: func(this goja.Value, _ ...goja.Value) (goja.Value, error) {
			return fn(this, vm.ToValue(p.now))
		}})
		return vm.ToValue(frame)
	})
	_ = vm.Set("cancelAnimationFrame", cancel)
}

// deliver hands the page a message, as the browser's postMessage to the Worker does.
func (p *page) deliver(msg map[string]any) error {
	ev := p.vm.NewObject()
	_ = ev.Set("data", js(p.vm, msg))
	for _, fn := range p.listen["message"] {
		if _, err := fn(goja.Undefined(), ev); err != nil {
			p.errs = append(p.errs, errText(err))
		}
	}
	return nil
}

// pump answers what the page sent, until it sends nothing more, then runs due timers.
func (p *page) pump() {
	for i := 0; i < 100000; i++ {
		p.unhandled()
		if len(p.outbox) > 0 {
			m := p.outbox[0]
			p.outbox = p.outbox[1:]
			p.handle(m)
			continue
		}
		next := p.nextTimer()
		if next == nil || next.at > p.now {
			return
		}
		p.fire(next)
	}
	p.errs = append(p.errs, "the page did not stop: it kept sending messages or scheduling timers")
}

func (p *page) unhandled() {
	for _, pr := range p.rejected {
		p.errs = append(p.errs, "unhandled rejection: "+jsError(p.vm, pr.Result()).Error())
	}
	p.rejected = nil
}

func (p *page) nextTimer() *timer {
	var best *timer
	live := p.timers[:0]
	for _, tm := range p.timers {
		if tm.canceled {
			continue
		}
		live = append(live, tm)
		if best == nil || tm.at < best.at {
			best = tm
		}
	}
	p.timers = live
	return best
}

func (p *page) fire(tm *timer) {
	if tm.every > 0 {
		tm.at += tm.every
	} else {
		tm.canceled = true
	}
	if _, err := tm.fn(goja.Undefined(), tm.args...); err != nil {
		p.errs = append(p.errs, errText(err))
	}
}

// settle lets the page run for ms of virtual time: its rpcs answer and its timers fire.
func (p *page) settle(ms float64) error {
	end := p.now + ms
	for i := 0; i < 100000; i++ {
		p.pump()
		next := p.nextTimer()
		if next == nil || next.at > end {
			break
		}
		p.now = next.at
	}
	p.now = end
	if len(p.errs) > p.seenErrs {
		msg := p.errs[p.seenErrs]
		p.seenErrs = len(p.errs)
		return fmt.Errorf("the page threw: %s", msg)
	}
	return nil
}

func (p *page) handle(m map[string]any) {
	switch m["t"] {
	case "ready":
		p.ready = true
	case "rpc":
		name, _ := m["name"].(string)
		id := m["id"]
		p.calls = append(p.calls, map[string]any{"name": name, "args": m["args"]})
		allowed := false
		for _, r := range p.def.RPC {
			allowed = allowed || r == name
		}
		var res map[string]any
		if !allowed {
			res = map[string]any{"lucode": 1, "t": "rpcres", "id": id, "ok": false,
				"error": fmt.Sprintf("the page %q cannot call %q: add it to the page's \"rpc\" in plugin.json", p.def.ID, name)}
		} else if v, err := p.t.rpc(name, m["args"], p.projectID()); err != nil {
			res = map[string]any{"lucode": 1, "t": "rpcres", "id": id, "ok": false, "error": err.Error()}
		} else {
			res = map[string]any{"lucode": 1, "t": "rpcres", "id": id, "ok": true, "value": v}
		}
		_ = p.deliver(res)
	case "error":
		msg, _ := m["message"].(string)
		p.errs = append(p.errs, msg)
	case "toast":
		p.toasts = append(p.toasts, map[string]any{"message": m["message"], "tone": m["tone"]})
	case "open":
		u, _ := m["url"].(string)
		p.opened = append(p.opened, u)
	case "evdone":
		if seq, ok := m["seq"].(float64); ok {
			p.prevent[int(seq)], _ = m["prevented"].(bool)
		}
	}
}

func (p *page) projectID() string {
	ctx := p.vm.Get("lucode").ToObject(p.vm).Get("context").ToObject(p.vm)
	return ctx.Get("projectId").String()
}

func (p *page) call(name string, args ...any) goja.Value {
	fn, _ := goja.AssertFunction(p.helper.Get(name))
	vals := make([]goja.Value, len(args))
	for i, a := range args {
		vals[i] = p.vm.ToValue(a)
	}
	v, err := fn(goja.Undefined(), vals...)
	if err != nil {
		msg, _, _ := strings.Cut(errText(err), "\n") // where it failed is the test's line, not the helper's
		throw(p.t.vm, "%s", msg)
	}
	return v
}

// event sends one event to a node, the way the browser's page does, and waits for the answer.
func (p *page) event(id int64, typ string, detail map[string]any, sync map[string]any) bool {
	p.evSeq++
	seq := p.evSeq
	msg := map[string]any{"lucode": 1, "t": "ev", "seq": seq, "target": id, "type": typ, "detail": detail}
	if sync != nil {
		msg["sync"] = sync
	}
	_ = p.deliver(msg)
	p.pump()
	return p.prevent[seq]
}

func (p *page) after() {
	if err := p.settle(1000); err != nil {
		throw(p.t.vm, "%s", err.Error())
	}
}

func (p *page) object() *goja.Object {
	vm := p.t.vm
	o := vm.NewObject()
	sel := func(c goja.FunctionCall) string { return c.Argument(0).String() }
	optSel := func(c goja.FunctionCall) any {
		if goja.IsUndefined(c.Argument(0)) || goja.IsNull(c.Argument(0)) {
			return nil
		}
		return c.Argument(0).String()
	}
	find := func(s string) int64 { return p.call("find", s).ToInteger() }

	_ = o.Set("html", func(c goja.FunctionCall) goja.Value { return vm.ToValue(p.call("html", optSel(c)).String()) })
	_ = o.Set("text", func(c goja.FunctionCall) goja.Value { return vm.ToValue(p.call("text", optSel(c)).String()) })
	_ = o.Set("count", func(c goja.FunctionCall) goja.Value { return vm.ToValue(p.call("count", sel(c)).ToInteger()) })
	_ = o.Set("exists", func(c goja.FunctionCall) goja.Value { return vm.ToValue(p.call("count", sel(c)).ToInteger() > 0) })
	_ = o.Set("attr", func(c goja.FunctionCall) goja.Value {
		v := p.call("attr", sel(c), c.Argument(1).String())
		if goja.IsNull(v) {
			return goja.Null()
		}
		return vm.ToValue(v.String())
	})
	_ = o.Set("value", func(c goja.FunctionCall) goja.Value { return vm.ToValue(p.call("value", sel(c)).String()) })
	_ = o.Set("checked", func(c goja.FunctionCall) goja.Value { return vm.ToValue(p.call("checked", sel(c)).ToBoolean()) })
	_ = o.Set("disabled", func(c goja.FunctionCall) goja.Value { return vm.ToValue(p.call("disabled", sel(c)).ToBoolean()) })

	click := func(s string) {
		id := find(s)
		if p.call("isDisabled", id).ToBoolean() {
			throw(vm, "%s is disabled, and the browser would not click it", s)
		}
		prevented := p.event(id, "click", map[string]any{"button": 0, "detail": 1}, nil)
		if form := p.call("submitFor", id).ToInteger(); form != 0 && !prevented {
			p.event(form, "submit", map[string]any{}, nil)
		}
		p.after()
	}
	_ = o.Set("click", func(c goja.FunctionCall) goja.Value { click(sel(c)); return goja.Undefined() })
	_ = o.Set("input", func(c goja.FunctionCall) goja.Value {
		id := find(sel(c))
		v := c.Argument(1).String()
		p.event(id, "input", map[string]any{"inputType": "insertText", "data": v}, map[string]any{"value": v})
		p.event(id, "change", map[string]any{}, map[string]any{"value": v})
		p.after()
		return goja.Undefined()
	})
	_ = o.Set("select", func(c goja.FunctionCall) goja.Value {
		id := find(sel(c))
		v := c.Argument(1).String()
		p.event(id, "input", map[string]any{}, map[string]any{"value": v})
		p.event(id, "change", map[string]any{}, map[string]any{"value": v})
		p.after()
		return goja.Undefined()
	})
	_ = o.Set("check", func(c goja.FunctionCall) goja.Value {
		id := find(sel(c))
		on := goja.IsUndefined(c.Argument(1)) || c.Argument(1).ToBoolean()
		p.event(id, "click", map[string]any{"button": 0}, map[string]any{"checked": on})
		p.event(id, "input", map[string]any{}, map[string]any{"checked": on})
		p.event(id, "change", map[string]any{}, map[string]any{"checked": on})
		p.after()
		return goja.Undefined()
	})
	_ = o.Set("submit", func(c goja.FunctionCall) goja.Value {
		p.event(find(sel(c)), "submit", map[string]any{}, nil)
		p.after()
		return goja.Undefined()
	})
	_ = o.Set("key", func(c goja.FunctionCall) goja.Value {
		id := find(sel(c))
		k := c.Argument(1).String()
		p.event(id, "keydown", map[string]any{"key": k, "code": k}, nil)
		p.event(id, "keyup", map[string]any{"key": k, "code": k}, nil)
		p.after()
		return goja.Undefined()
	})
	_ = o.Set("dispatch", func(c goja.FunctionCall) goja.Value {
		detail, _ := plain(vm, c.Argument(2))
		d, _ := detail.(map[string]any)
		p.event(find(sel(c)), c.Argument(1).String(), d, nil)
		p.after()
		return goja.Undefined()
	})
	_ = o.Set("emit", func(c goja.FunctionCall) goja.Value {
		data, _ := plain(vm, c.Argument(1))
		_ = p.deliver(map[string]any{"lucode": 1, "t": "event", "name": c.Argument(0).String(), "data": data})
		p.after()
		return goja.Undefined()
	})
	_ = o.Set("wait", func(c goja.FunctionCall) goja.Value {
		ms := c.Argument(0).ToFloat()
		if err := p.settle(max(ms, 0)); err != nil {
			throw(vm, "%s", err.Error())
		}
		return goja.Undefined()
	})
	_ = o.Set("toasts", func(goja.FunctionCall) goja.Value { return js(vm, p.toasts) })
	_ = o.Set("opened", func(goja.FunctionCall) goja.Value { return js(vm, p.opened) })
	_ = o.Set("calls", func(goja.FunctionCall) goja.Value { return js(vm, p.calls) })
	_ = o.Set("errors", func(goja.FunctionCall) goja.Value { return js(vm, p.errs) })
	return o
}
