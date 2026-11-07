package plugins

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path"
	"reflect"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/dlclark/regexp2"
	"github.com/dop251/goja"
)

// Limits of one invocation of plugin code.
const (
	MaxCodeBytes     = 5 << 20  // one .js file
	MaxPayloadBytes  = 32 << 20 // what an invocation receives or returns (a turn can carry an image)
	MaxCallBytes     = 16 << 20 // one value through a host function
	maxCallStack     = 2000     // call depth of the script
	maxConcurrent    = 8        // invocations of one plugin at a time; the rest queue
	interruptGrace   = 3 * time.Second
	maxRegexpSeconds = 5
)

// RPCSeconds and EventSeconds bound what the manifest does not.
const (
	RPCSeconds   = 15
	EventSeconds = 10
)

//go:embed harden.js
var hardenSource string

var hardenProgram = goja.MustCompile("runtime", hardenSource, false)

var setupOnce sync.Once

// setupProcess sets what only makes sense once per process. Regexes of the script engine that
// need backtracking stop on their own. A native recursion that outruns the heap watchdog gets
// more room before the Go runtime would kill the whole process over it.
func setupProcess() {
	setupOnce.Do(func() {
		regexp2.DefaultMatchTimeout = maxRegexpSeconds * time.Second
		debug.SetMaxStack(4 << 30)
	})
}

// ErrNoHandler means the plugin exports nothing under that name.
var ErrNoHandler = errors.New("the plugin has no handler for this")

// Error is what a plugin's failure looks like outside. Its text is safe to show.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// the engine's name never reaches a user, including from its own messages
const engineName = "g" + "oja"

var (
	engineNameRe = regexp.MustCompile(`(?i)github\.com/dop251/` + engineName + `[^\s:]*|` + engineName)
	goFileRe     = regexp.MustCompile(`[^\s"']*\.go:\d+`)
)

// Scrub removes what would give away the host from a message.
func Scrub(s string) string {
	s = engineNameRe.ReplaceAllString(s, "plugin runtime")
	s = goFileRe.ReplaceAllString(s, "")
	return s
}

// Options make a Runtime.
type Options struct {
	Package *Package
	Target  string // the target this Runtime runs
	HTTP    HTTPOptions
	// ScrubLog removes secrets from anything that ends up in the plugin's log.
	ScrubLog func(string) string
	// HeapCap overrides the heap watchdog's cap (tests).
	HeapCap uint64
}

// Runtime holds what one plugin's invocations share on this machine: its compiled code, its
// memory, its locks, its log. No script state survives between invocations.
type Runtime struct {
	opts Options
	sem  chan struct{}
	life context.Context
	stop context.CancelFunc

	progMu   sync.Mutex
	programs map[string]*goja.Program

	mem   memoryStore
	locks lockTable
	logs  logRing

	httpOnce sync.Once
	httpC    *httpCaller
}

// NewRuntime makes the Runtime of a loaded plugin.
func NewRuntime(o Options) *Runtime {
	setupProcess()
	life, stop := context.WithCancel(context.Background())
	return &Runtime{
		opts: o, sem: make(chan struct{}, maxConcurrent), life: life, stop: stop,
		programs: map[string]*goja.Program{}, mem: memoryStore{m: map[string][]byte{}},
		locks: lockTable{m: map[string]chan struct{}{}},
	}
}

// Close cancels everything the plugin has running: it was disabled or removed.
func (r *Runtime) Close() { r.stop() }

// LogsSince returns the log lines after a count of lines ever written, and the new count.
func (r *Runtime) LogsSince(seq int) ([]LogEntry, int) { return r.logs.since(seq) }

// Manifest is the plugin's manifest.
func (r *Runtime) Manifest() Manifest { return r.opts.Package.Manifest }

// Package is the plugin's package.
func (r *Runtime) Package() *Package { return r.opts.Package }

// Logs copies the plugin's log.
func (r *Runtime) Logs() []LogEntry { return r.logs.snapshot() }

// Log adds a line to the plugin's log.
func (r *Runtime) Log(level, msg string) {
	msg = Scrub(msg)
	if r.opts.ScrubLog != nil {
		msg = r.opts.ScrubLog(msg)
	}
	r.logs.add(level, msg)
}

// Context is what lucode.context reports.
type Context struct {
	ProjectID string `json:"projectId"`
	SessionID string `json:"sessionId"`
	CardID    string `json:"cardId,omitempty"`
	Kind      string `json:"kind"`
	// The chain that led here, so what the plugin does counts towards the flows' loop limit.
	FlowID string `json:"-"`
	RunID  string `json:"-"`
	Depth  int    `json:"-"`
}

// Invocation is one call into plugin code.
type Invocation struct {
	Path     []string // the exported function: rpc, name
	Arg      any      // JSON-plain
	Timeout  time.Duration
	Context  Context
	Perms    Permissions // the target's effective permissions
	Modules  []Module    // the host's part of the SDK
	Progress func(string)
}

// Invoke runs one exported function in a fresh script VM and returns its JSON-plain result.
// The context, the deadline and the plugin's own lifetime all interrupt it, and a failure of
// the code never gets past this function.
func (r *Runtime) Invoke(ctx context.Context, inv Invocation) (any, error) {
	if inv.Timeout <= 0 {
		inv.Timeout = RPCSeconds * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, inv.Timeout)
	defer cancel()
	stopLife := context.AfterFunc(r.life, cancel)
	defer stopLife()

	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, r.stopError(ctx, inv, "while waiting for its turn")
	}

	arg, err := plainCopy(inv.Arg, MaxPayloadBytes)
	if err != nil {
		<-r.sem
		return nil, &Error{Msg: "the plugin's arguments: " + err.Error()}
	}
	inv.Arg = arg

	vm := goja.New()
	vm.SetMaxCallStackSize(maxCallStack)

	type outcome struct {
		value any
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		defer func() { <-r.sem }() // a call the VM cannot leave keeps its slot: only this plugin waits
		defer func() {
			if p := recover(); p != nil {
				log.Printf("[plugins] %s panic: %s", r.Manifest().ID, Scrub(fmt.Sprint(p)))
				done <- outcome{err: &Error{Msg: "the plugin runtime hit an internal error"}}
			}
		}()
		v, err := r.run(ctx, vm, inv)
		done <- outcome{v, err}
	}()

	heapStop := watchHeap(r.opts.HeapCap, func() { vm.Interrupt(errHeap) })
	defer heapStop()

	interrupted := false
	for {
		var ctxDone <-chan struct{}
		if !interrupted {
			ctxDone = ctx.Done()
		}
		select {
		case o := <-done:
			return r.finish(ctx, inv, o.value, o.err)
		case <-ctxDone:
			vm.Interrupt(errStopped)
			interrupted = true
		case <-afterIf(interrupted):
			// inside a native call that does not check: give up on it, the turn must not stick
			r.Log("error", "stopped waiting for code that does not respond to its deadline")
			return nil, r.stopError(ctx, inv, "")
		}
	}
}

func afterIf(on bool) <-chan time.Time {
	if on {
		return time.After(interruptGrace)
	}
	return nil
}

var (
	errHeap    = errors.New("used too much memory")
	errStopped = errors.New("stopped")
)

func (r *Runtime) stopError(ctx context.Context, inv Invocation, suffix string) error {
	msg := "the plugin was stopped"
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		msg = fmt.Sprintf("the plugin ran out of time (%s)", inv.Timeout.Round(time.Millisecond))
	}
	if suffix != "" {
		msg += " " + suffix
	}
	return &Error{Msg: msg}
}

func (r *Runtime) finish(ctx context.Context, inv Invocation, v any, err error) (any, error) {
	if err == nil {
		return v, nil
	}
	if errors.Is(err, ErrNoHandler) {
		return nil, err
	}
	var ie *goja.InterruptedError
	if errors.As(err, &ie) {
		if ie.Value() == errHeap {
			msg := "the plugin used too much memory and was stopped"
			r.Log("error", msg)
			return nil, &Error{Msg: msg}
		}
		return nil, r.stopError(ctx, inv, "")
	}
	var pe *Error
	if !errors.As(err, &pe) {
		pe = &Error{Msg: Scrub(err.Error())}
	}
	if r.opts.ScrubLog != nil {
		pe = &Error{Msg: r.opts.ScrubLog(pe.Msg)}
	}
	r.Log("error", strings.Join(inv.Path, ".")+": "+pe.Msg)
	return nil, pe
}

// run happens on the VM's own goroutine.
func (r *Runtime) run(ctx context.Context, vm *goja.Runtime, inv Invocation) (any, error) {
	s := &Scope{VM: vm, Ctx: ctx, RT: r, Inv: inv, modules: map[string]goja.Value{}}
	if err := s.boot(); err != nil {
		return nil, err
	}
	manifest := r.Manifest()
	main := manifest.Target(r.opts.Target)
	if main == nil {
		return nil, &Error{Msg: "this plugin has no " + r.opts.Target + " target"}
	}
	exports, err := s.require("", main.Main)
	if err != nil {
		return nil, err
	}
	var this goja.Value = goja.Undefined()
	cur := exports
	for _, key := range inv.Path {
		obj, ok := cur.(*goja.Object)
		if !ok {
			return nil, ErrNoHandler
		}
		this = obj
		cur = obj.Get(key)
		if cur == nil || goja.IsUndefined(cur) || goja.IsNull(cur) {
			return nil, ErrNoHandler
		}
	}
	fn, ok := goja.AssertFunction(cur)
	if !ok {
		return nil, ErrNoHandler
	}
	res, err := fn(this, s.JS(inv.Arg))
	if err != nil {
		return nil, scriptError(err)
	}
	res, err = settle(res)
	if err != nil {
		return nil, err
	}
	return s.plain(res, MaxPayloadBytes)
}

// settle reads an async handler's answer. Nothing a plugin can call stays pending across the end
// of the call, since there are no timers: a promise still pending never will be settled.
func settle(v goja.Value) (goja.Value, error) {
	if v == nil {
		return goja.Undefined(), nil
	}
	if v.ExportType() != reflect.TypeOf((*goja.Promise)(nil)) {
		return v, nil
	}
	p, ok := v.Export().(*goja.Promise)
	if !ok {
		return v, nil
	}
	switch p.State() {
	case goja.PromiseStateFulfilled:
		return p.Result(), nil
	case goja.PromiseStateRejected:
		return nil, &Error{Msg: Scrub(safeText(p.Result()))}
	}
	return nil, &Error{Msg: "the plugin returned a promise that never settles"}
}

func safeText(v goja.Value) (out string) {
	defer func() {
		if p := recover(); p != nil {
			out = "the plugin threw something that cannot be read"
		}
	}()
	if v == nil {
		return "unknown error"
	}
	return v.String()
}

// scriptError turns an engine error into something safe, keeping interrupts as they are.
func scriptError(err error) error {
	var ie *goja.InterruptedError
	if errors.As(err, &ie) {
		return ie
	}
	var so *goja.StackOverflowError
	if errors.As(err, &so) {
		return &Error{Msg: "the plugin went too deep (call stack limit)"}
	}
	var ex *goja.Exception
	if errors.As(err, &ex) {
		return &Error{Msg: Scrub(safeText(ex.Value()))}
	}
	return &Error{Msg: Scrub(err.Error())}
}

func (r *Runtime) program(rel string) (*goja.Program, error) {
	r.progMu.Lock()
	p, ok := r.programs[rel]
	r.progMu.Unlock()
	if ok {
		return p, nil
	}
	src, err := r.opts.Package.ReadFile(rel, MaxCodeBytes)
	if err != nil {
		return nil, &Error{Msg: Scrub(err.Error())}
	}
	wrapped := "(function(module,exports,require){" + string(src) + "\n})"
	if err := checkLiterals(rel, wrapped); err != nil {
		return nil, &Error{Msg: "the plugin's code cannot run: " + err.Error()}
	}
	p, err = goja.Compile(rel, wrapped, false)
	if err != nil {
		return nil, &Error{Msg: "the plugin's code does not compile: " + Scrub(err.Error())}
	}
	r.progMu.Lock()
	r.programs[rel] = p
	r.progMu.Unlock()
	return p, nil
}

// Scope is one invocation's VM with its SDK.
type Scope struct {
	VM  *goja.Runtime
	Ctx context.Context
	RT  *Runtime
	Inv Invocation

	stringify goja.Callable
	parse     goja.Callable
	modules   map[string]goja.Value // loaded files, for require
	held      map[string]bool       // locks this invocation holds
}

// boot hardens the fresh VM and installs the SDK. It runs before any plugin code.
func (s *Scope) boot() error {
	boot, err := s.VM.RunProgram(hardenProgram)
	if err != nil {
		return scriptError(err)
	}
	fn, ok := goja.AssertFunction(boot)
	if !ok {
		return &Error{Msg: "the plugin runtime did not start"}
	}
	check := func(c goja.FunctionCall) goja.Value {
		return s.VM.ToValue(jsonDepthOK(c.Argument(0).String()))
	}
	res, err := fn(goja.Undefined(), s.VM.ToValue(check))
	if err != nil {
		return scriptError(err)
	}
	obj := res.(*goja.Object)
	s.stringify, _ = goja.AssertFunction(obj.Get("stringify"))
	s.parse, _ = goja.AssertFunction(obj.Get("parse"))
	s.installSDK()
	return nil
}

// jsonDepthOK refuses text nested deeper than a person writes, before a parser recurses on it.
func jsonDepthOK(text string) bool {
	depth, inString, escaped := 0, false, false
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case inString:
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
		case c == '"':
			inString = true
		case c == '[' || c == '{':
			if depth++; depth > 512 {
				return false
			}
		case c == ']' || c == '}':
			depth--
		}
	}
	return true
}

// require loads a file of the zip, the only code a plugin can reach.
func (s *Scope) require(from, spec string) (goja.Value, error) {
	rel := spec
	if from != "" {
		if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
			return nil, &Error{Msg: fmt.Sprintf("require(%q): only files inside the plugin can be loaded, with a path starting with ./", spec)}
		}
		rel = path.Join(path.Dir(from), spec)
	}
	if path.Ext(rel) == "" {
		rel += ".js"
	}
	if err := checkPluginPath(rel); err != nil {
		return nil, &Error{Msg: fmt.Sprintf("require(%q): only files inside the plugin can be loaded", spec)}
	}
	if path.Ext(rel) != ".js" && path.Ext(rel) != ".json" {
		return nil, &Error{Msg: fmt.Sprintf("require(%q): only .js and .json files can be loaded", spec)}
	}
	module := s.VM.NewObject()
	exports := s.VM.NewObject()
	if v, ok := s.modules[rel]; ok {
		return v.(*goja.Object).Get("exports"), nil
	}
	_ = module.Set("exports", exports)
	s.modules[rel] = module
	if path.Ext(rel) == ".json" {
		raw, err := s.RT.opts.Package.ReadFile(rel, MaxFileBytes)
		if err != nil {
			return nil, &Error{Msg: fmt.Sprintf("require(%q): no such file in the plugin", spec)}
		}
		v, err := s.fromJSON(raw)
		if err != nil {
			return nil, &Error{Msg: fmt.Sprintf("require(%q): invalid JSON", spec)}
		}
		_ = module.Set("exports", v)
		return v, nil
	}
	prog, err := s.RT.program(rel)
	if err != nil {
		return nil, err
	}
	wrapped, err := s.VM.RunProgram(prog)
	if err != nil {
		return nil, scriptError(err)
	}
	fn, ok := goja.AssertFunction(wrapped)
	if !ok {
		return nil, &Error{Msg: "the plugin's code does not compile"}
	}
	req := func(c goja.FunctionCall) goja.Value {
		v, err := s.require(rel, c.Argument(0).String())
		if err != nil {
			s.rethrow(err)
		}
		return v
	}
	if _, err := fn(goja.Undefined(), module, exports, s.VM.ToValue(req)); err != nil {
		return nil, scriptError(err)
	}
	return module.Get("exports"), nil
}

// rethrow raises an error inside the script, or stops it if the error is an interrupt.
func (s *Scope) rethrow(err error) {
	var ie *goja.InterruptedError
	if errors.As(err, &ie) {
		panic(ie)
	}
	var ex *goja.Exception
	if errors.As(err, &ex) {
		panic(ex.Value())
	}
	s.Throw("%s", err.Error())
}

// Throw raises a JavaScript error with this text. It does not return.
func (s *Scope) Throw(format string, args ...any) {
	panic(s.VM.NewTypeError("%s", Scrub(fmt.Sprintf(format, args...))))
}

// JS gives the script a JSON-plain Go value as new script objects, never as a wrapper around
// the Go value.
func (s *Scope) JS(v any) goja.Value {
	switch x := v.(type) {
	case nil:
		return goja.Null()
	case string:
		return s.VM.ToValue(x)
	case bool:
		return s.VM.ToValue(x)
	case float64:
		return s.VM.ToValue(x)
	case int:
		return s.VM.ToValue(x)
	case int64:
		return s.VM.ToValue(x)
	case []any:
		items := make([]any, len(x))
		for i, e := range x {
			items[i] = s.JS(e)
		}
		return s.VM.NewArray(items...)
	case map[string]any:
		obj := s.VM.NewObject()
		for k, e := range x {
			_ = obj.Set(k, s.JS(e))
		}
		return obj
	}
	// anything else goes through JSON first, so nothing with methods gets in
	clean, err := plainCopy(v, MaxPayloadBytes)
	if err != nil {
		s.Throw("this value cannot be given to a plugin")
	}
	return s.JS(clean)
}

func (s *Scope) fromJSON(raw []byte) (goja.Value, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return s.JS(v), nil
}

// plain reads a script value as JSON-plain data. The script's own serializer does it, so a deep
// or endless value hits the call stack limit instead of the Go stack.
func (s *Scope) plain(v goja.Value, limit int) (any, error) {
	if v == nil || goja.IsUndefined(v) {
		return nil, nil
	}
	text, err := s.stringify(goja.Undefined(), v)
	if err != nil {
		return nil, scriptError(err)
	}
	if goja.IsUndefined(text) {
		return nil, nil
	}
	body := text.String()
	if len(body) > limit {
		return nil, &Error{Msg: fmt.Sprintf("a value of the plugin is larger than %d MB", limit>>20)}
	}
	var out any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		return nil, &Error{Msg: "a value of the plugin is not plain JSON"}
	}
	return out, nil
}

// Plain is plain for a value given to a host function; failures throw.
func (s *Scope) Plain(v goja.Value) any {
	out, err := s.plain(v, MaxCallBytes)
	if err != nil {
		s.rethrow(err)
	}
	return out
}

// Str reads a required string argument.
func (s *Scope) Str(c goja.FunctionCall, i int, name string) string {
	v := c.Argument(i)
	if goja.IsUndefined(v) || goja.IsNull(v) {
		s.Throw("%s is required", name)
	}
	if v.ExportType() != reflect.TypeOf("") {
		s.Throw("%s must be a string", name)
	}
	out := v.String()
	if len(out) > MaxCallBytes {
		s.Throw("%s is too large", name)
	}
	return out
}

// Obj reads an optional options argument as a plain map.
func (s *Scope) Obj(c goja.FunctionCall, i int) map[string]any {
	v := c.Argument(i)
	if goja.IsUndefined(v) || goja.IsNull(v) {
		return map[string]any{}
	}
	m, ok := s.Plain(v).(map[string]any)
	if !ok {
		s.Throw("expected an object of options")
	}
	return m
}

// Wait runs slow host work away from the VM's goroutine, so the deadline can give up on it even
// when the work itself cannot be cancelled. Failures throw.
func Wait[T any](s *Scope, work func(ctx context.Context) (T, error)) T {
	type res struct {
		v   T
		err error
	}
	ch := make(chan res, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				ch <- res{err: errors.New("the host call failed")}
			}
		}()
		v, err := work(s.Ctx)
		ch <- res{v, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			s.Throw("%s", r.err.Error())
		}
		return r.v
	case <-s.Ctx.Done():
		s.Throw("cancelled")
		panic("unreachable")
	}
}

// plainCopy turns any value into JSON-plain data, or fails if it is larger than limit.
func plainCopy(v any, limit int) (any, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, errors.New("not plain JSON")
	}
	if len(raw) > limit {
		return nil, fmt.Errorf("larger than %d MB", limit>>20)
	}
	var out any
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&out); err != nil {
		return nil, errors.New("not plain JSON")
	}
	return out, nil
}
