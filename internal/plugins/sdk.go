package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
)

// The SDK a plugin sees as the global lucode. The base here is the same on both targets. Each
// host adds its own modules; whatever belongs to the other target stays listed and throws a
// clear error, so a plugin never just finds undefined.

const (
	maxMemoryBytes = 16 << 20
	maxLogLines    = 500
	maxLogLine     = 4 << 10
	maxDataBytes   = 20 << 20
	progressEvery  = 200 * time.Millisecond
)

// Func is a host function of the SDK.
type Func func(s *Scope, c goja.FunctionCall) goja.Value

// Module is a part of the SDK a host provides.
type Module struct {
	Name  string
	Funcs map[string]Func
}

// modules of the SDK that belong to one target
var targetModules = map[string]map[string][]string{
	TargetServer: {
		"settings":      {"get", "all"},
		"storage":       {"get", "set", "delete", "list"},
		"conversations": {"get", "list", "save", "bind"},
		"sessions":      {"get", "messages"},
		"board":         {"listLists", "listCards", "getCard", "createCard", "moveCard", "comment", "addTag"},
		"flows":         {"emit"},
		"browser": {"launch", "close", "status", "tabs", "newTab", "switchTab", "closeTab", "info",
			"navigate", "find", "click", "type", "press", "evalPage", "text"},
		"runners": {"list", "call"},
		"relay":   {"status", "createEndpoint", "listEndpoints", "deleteEndpoint"},
	},
	TargetRunner: {
		"fs":     {"read", "write", "list", "exists", "stat"},
		"git":    {"status", "diff", "log", "branch"},
		"exec":   {"run"},
		"server": {"call"},
	},
}

func (s *Scope) installSDK() {
	vm := s.VM
	m := s.RT.Manifest()
	sdk := vm.NewObject()

	plugin := vm.NewObject()
	_ = plugin.Set("id", m.ID)
	_ = plugin.Set("name", m.Name)
	_ = plugin.Set("version", m.Version)
	_ = sdk.Set("plugin", plugin)

	ctx := vm.NewObject()
	_ = ctx.Set("projectId", s.Inv.Context.ProjectID)
	_ = ctx.Set("sessionId", s.Inv.Context.SessionID)
	_ = ctx.Set("cardId", s.Inv.Context.CardID)
	_ = ctx.Set("kind", s.Inv.Context.Kind)
	_ = ctx.Set("target", s.RT.opts.Target)
	left := int64(0)
	if d, ok := s.Ctx.Deadline(); ok {
		left = time.Until(d).Milliseconds()
	}
	_ = ctx.Set("deadlineMs", left)
	_ = ctx.Set("cancelled", vm.ToValue(func(goja.FunctionCall) goja.Value { return vm.ToValue(s.Ctx.Err() != nil) }))
	var lastProgress time.Time
	_ = ctx.Set("progress", vm.ToValue(func(c goja.FunctionCall) goja.Value {
		// each one goes to every browser watching: a loop must not turn into a flood
		if s.Inv.Progress != nil && time.Since(lastProgress) >= progressEvery {
			lastProgress = time.Now()
			text := c.Argument(0).String()
			if len(text) > 500 {
				text = text[:500]
			}
			s.Inv.Progress(text)
		}
		return goja.Undefined()
	}))
	_ = sdk.Set("context", ctx)

	log := vm.NewObject()
	for _, level := range []string{"info", "warn", "error"} {
		level := level
		_ = log.Set(level, vm.ToValue(func(c goja.FunctionCall) goja.Value {
			parts := make([]string, 0, len(c.Arguments))
			for _, a := range c.Arguments {
				if a.ExportType().Kind().String() == "string" {
					parts = append(parts, a.String())
					continue
				}
				if text, err := s.plain(a, maxLogLine); err == nil {
					raw, _ := json.Marshal(text)
					parts = append(parts, string(raw))
				} else {
					parts = append(parts, "(a value that cannot be written)")
				}
			}
			s.RT.Log(level, strings.Join(parts, " "))
			return goja.Undefined()
		}))
	}
	_ = sdk.Set("log", log)

	s.installModule(sdk, s.dataModule())
	s.installModule(sdk, s.memoryModule())
	s.installModule(sdk, s.timeModule())
	s.installModule(sdk, s.locksModule())
	s.installModule(sdk, s.httpModule())

	given := map[string]bool{}
	for _, mod := range s.Inv.Modules {
		given[mod.Name] = true
		s.installModule(sdk, mod)
	}
	// the other target's functions, and this target's that the host did not give
	for target, mods := range targetModules {
		for name, funcs := range mods {
			if given[name] {
				continue
			}
			msg := fmt.Sprintf("lucode.%s is not available here", name)
			if target != s.RT.opts.Target {
				msg = fmt.Sprintf("lucode.%s only works in the %s target, and this code runs in the %s target", name, target, s.RT.opts.Target)
			}
			stub := vm.NewObject()
			for _, fn := range funcs {
				fn, msg := fn, msg
				_ = stub.Set(fn, vm.ToValue(func(goja.FunctionCall) goja.Value {
					s.Throw("%s (%s)", msg, fn)
					return nil
				}))
			}
			_ = sdk.Set(name, stub)
		}
	}
	_ = vm.Set("lucode", sdk)
}

func (s *Scope) installModule(sdk *goja.Object, m Module) {
	obj := s.VM.NewObject()
	for name, fn := range m.Funcs {
		fn := fn
		_ = obj.Set(name, s.VM.ToValue(func(c goja.FunctionCall) goja.Value { return fn(s, c) }))
	}
	_ = sdk.Set(m.Name, obj)
}

// ---- log ----

// LogEntry is a line of a plugin's log.
type LogEntry struct {
	Time  string `json:"time"`
	Level string `json:"level"`
	Text  string `json:"text"`
}

type logRing struct {
	mu    sync.Mutex
	lines []LogEntry
	total int // lines ever written
}

// since returns the lines after the given count of lines ever written, and the new count.
func (l *logRing) since(seq int) ([]LogEntry, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := l.total - seq
	if n <= 0 {
		return nil, l.total
	}
	if n > len(l.lines) {
		n = len(l.lines)
	}
	return append([]LogEntry{}, l.lines[len(l.lines)-n:]...), l.total
}

// add keeps a line and returns it as kept (cut to the line limit).
func (l *logRing) add(level, text string) LogEntry {
	if len(text) > maxLogLine {
		text = text[:maxLogLine] + "..."
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.total++
	e := LogEntry{Time: time.Now().UTC().Format(time.RFC3339), Level: level, Text: text}
	l.lines = append(l.lines, e)
	if len(l.lines) > maxLogLines {
		l.lines = append([]LogEntry(nil), l.lines[len(l.lines)-maxLogLines:]...)
	}
	return e
}

func (l *logRing) snapshot() []LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]LogEntry{}, l.lines...)
}

// ---- data ----

func (s *Scope) dataPath(c goja.FunctionCall) string {
	return "data/" + strings.TrimPrefix(s.Str(c, 0, "path"), "data/")
}

func (s *Scope) dataModule() Module {
	read := func(s *Scope, c goja.FunctionCall) []byte {
		raw, err := s.RT.opts.Package.ReadFile(s.dataPath(c), maxDataBytes)
		if err != nil {
			s.Throw("%s", err.Error())
		}
		return raw
	}
	return Module{Name: "data", Funcs: map[string]Func{
		"text": func(s *Scope, c goja.FunctionCall) goja.Value { return s.VM.ToValue(string(read(s, c))) },
		"json": func(s *Scope, c goja.FunctionCall) goja.Value {
			v, err := s.fromJSON(read(s, c))
			if err != nil {
				s.Throw("this file is not valid JSON")
			}
			return v
		},
		"list": func(s *Scope, c goja.FunctionCall) goja.Value {
			dir := "data"
			if a := c.Argument(0); !goja.IsUndefined(a) && a.String() != "" {
				dir = "data/" + strings.TrimPrefix(strings.TrimSuffix(a.String(), "/"), "data/")
			}
			files, err := s.RT.opts.Package.ListFiles(dir)
			if err != nil {
				s.Throw("%s", err.Error())
			}
			out := make([]any, 0, len(files))
			for _, f := range files {
				out = append(out, strings.TrimPrefix(f, "data/"))
			}
			return s.JS(out)
		},
	}}
}

// ---- memory ----

type memoryStore struct {
	mu   sync.Mutex
	m    map[string][]byte
	size int
}

func (s *Scope) memoryModule() Module {
	mem := &s.RT.mem
	return Module{Name: "memory", Funcs: map[string]Func{
		"get": func(s *Scope, c goja.FunctionCall) goja.Value {
			key := s.Str(c, 0, "key")
			mem.mu.Lock()
			raw, ok := mem.m[key]
			mem.mu.Unlock()
			if !ok {
				return goja.Null()
			}
			v, err := s.fromJSON(raw)
			if err != nil {
				return goja.Null()
			}
			return v
		},
		"set": func(s *Scope, c goja.FunctionCall) goja.Value {
			key := s.Str(c, 0, "key")
			raw, err := json.Marshal(s.Plain(c.Argument(1)))
			if err != nil {
				s.Throw("this value cannot be kept")
			}
			mem.mu.Lock()
			defer mem.mu.Unlock()
			grown := mem.size - len(mem.m[key]) + len(raw) + len(key)
			if _, had := mem.m[key]; had {
				grown -= len(key)
			}
			if grown > maxMemoryBytes {
				s.Throw("lucode.memory is full (%d MB)", maxMemoryBytes>>20)
			}
			mem.m[key], mem.size = raw, grown
			return goja.Undefined()
		},
		"delete": func(s *Scope, c goja.FunctionCall) goja.Value {
			key := s.Str(c, 0, "key")
			mem.mu.Lock()
			defer mem.mu.Unlock()
			if raw, ok := mem.m[key]; ok {
				mem.size -= len(raw) + len(key)
				delete(mem.m, key)
			}
			return goja.Undefined()
		},
	}}
}

// ---- time ----

func (s *Scope) timeModule() Module {
	return Module{Name: "time", Funcs: map[string]Func{
		"now": func(s *Scope, _ goja.FunctionCall) goja.Value { return s.VM.ToValue(time.Now().UnixMilli()) },
		"sleep": func(s *Scope, c goja.FunctionCall) goja.Value {
			ms := c.Argument(0).ToInteger()
			if ms <= 0 {
				return goja.Undefined()
			}
			t := time.NewTimer(time.Duration(ms) * time.Millisecond)
			defer t.Stop()
			select {
			case <-t.C:
			case <-s.Ctx.Done():
				s.Throw("cancelled")
			}
			return goja.Undefined()
		},
	}}
}

// ---- locks ----

type lockTable struct {
	mu sync.Mutex
	m  map[string]chan struct{}
}

func (l *lockTable) get(name string) chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	ch, ok := l.m[name]
	if !ok {
		ch = make(chan struct{}, 1)
		l.m[name] = ch
	}
	return ch
}

func (s *Scope) locksModule() Module {
	return Module{Name: "locks", Funcs: map[string]Func{
		"with": func(s *Scope, c goja.FunctionCall) goja.Value {
			name := s.Str(c, 0, "name")
			fn, ok := goja.AssertFunction(c.Argument(1))
			if !ok {
				s.Throw("lucode.locks.with needs a function")
			}
			if s.held[name] {
				s.Throw("this invocation already holds the lock %q", name)
			}
			ch := s.RT.locks.get(name)
			select {
			case ch <- struct{}{}:
			case <-s.Ctx.Done():
				s.Throw("cancelled while waiting for the lock %q", name)
			}
			defer func() { <-ch }()
			if s.held == nil {
				s.held = map[string]bool{}
			}
			s.held[name] = true
			defer delete(s.held, name)
			res, err := fn(goja.Undefined())
			if err != nil {
				s.rethrow(err)
			}
			return res
		},
	}}
}

// ---- http ----

func (s *Scope) httpModule() Module {
	return Module{Name: "http", Funcs: map[string]Func{
		"fetch": func(s *Scope, c goja.FunctionCall) goja.Value {
			perm := s.Inv.Perms.HTTP
			if perm == nil || len(perm.Hosts) == 0 {
				s.Throw("lucode.http needs the http permission")
			}
			url := s.Str(c, 0, "url")
			o := s.Obj(c, 1)
			req := FetchRequest{URL: url, Hosts: perm.Hosts}
			req.Method, _ = o["method"].(string)
			req.Body, _ = o["body"].(string)
			if ms, ok := o["timeoutMs"].(float64); ok {
				req.Timeout = time.Duration(ms) * time.Millisecond
			}
			if h, ok := o["headers"].(map[string]any); ok {
				req.Headers = map[string]string{}
				for k, v := range h {
					req.Headers[k] = fmt.Sprint(v)
				}
			}
			s.RT.httpOnce.Do(func() { s.RT.httpC = newHTTPCaller(s.RT.opts.HTTP) })
			res := Wait(s, func(ctx context.Context) (map[string]any, error) { return s.RT.httpC.fetch(ctx, req) })
			return s.JS(res)
		},
	}}
}

// SortedKeys returns a map's keys in order, for answers that must be stable.
func SortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
