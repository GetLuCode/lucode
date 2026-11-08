package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testPackage writes a package to disk the way an install leaves it.
func testPackage(t *testing.T, manifest string, files map[string]string) *Package {
	t.Helper()
	dir := t.TempDir()
	files["plugin.json"] = manifest
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m, err := ParseManifest([]byte(manifest), ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return &Package{Dir: dir, Manifest: m}
}

const simpleManifest = `{"id":"acme.test","name":"T","version":"1.0.0","sdk":1,"main":"main.js"}`

func runner(t *testing.T, code string) *Runtime {
	t.Helper()
	pkg := testPackage(t, simpleManifest, map[string]string{"main.js": code})
	return NewRuntime(Options{Package: pkg, Target: TargetServer})
}

func call(t *testing.T, r *Runtime, timeout time.Duration, arg any) (any, error) {
	t.Helper()
	return r.Invoke(context.Background(), Invocation{Path: []string{"rpc", "go"}, Timeout: timeout, Arg: arg})
}

// Every way of running text as code is gone, and every way of allocating without asking.
func TestSandboxTakesAwayTheDangerousGlobals(t *testing.T) {
	cases := []struct{ name, expr, want string }{
		{"eval", `typeof eval`, "undefined"},
		{"Function", `typeof Function`, "undefined"},
		{"Proxy", `typeof Proxy`, "undefined"},
		{"ArrayBuffer", `typeof ArrayBuffer`, "undefined"},
		{"Uint8Array", `typeof Uint8Array`, "undefined"},
		{"require global", `typeof process + typeof fs + typeof net + typeof setTimeout`, "undefinedundefinedundefinedundefined"},
		{"function constructor", `(function(){}).constructor === Object`, "true"},
		{"arrow constructor", `(()=>1).constructor('return 42')+''`, "return 42"},
		{"generator constructor", `(function*(){}).constructor === Object`, "true"},
		{"async constructor", `(async function(){}).constructor === Object`, "true"},
		{"proto constructor", `Object.getPrototypeOf(function(){}).constructor === Object`, "true"},
		{"reflect construct", `(function(){try{return Reflect.construct(Object.getPrototypeOf(function(){}).constructor,["return 7"])+''}catch(e){return "threw"}})()`, "return 7"}, // Object("return 7"): text, never code
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := runner(t, `module.exports={rpc:{go(){return String(`+c.expr+`)}}}`)
			v, err := call(t, r, time.Second*5, nil)
			if err != nil {
				t.Fatal(err)
			}
			if v != c.want {
				t.Fatalf("got %v, want %v", v, c.want)
			}
		})
	}
}

// Code that tries to take the process down only fails its own invocation. Each of these used to be
// a way to overflow the Go stack, run out of memory or spin forever inside a native call.
func TestSandboxSurvivesHostileCode(t *testing.T) {
	cases := []struct {
		name, code string
		want       string // part of the error; "a|b" accepts either
	}{
		{"endless loop", `for(;;){}`, "ran out of time"},
		{"endless recursion", `function f(){return f()+1};f()`, "too deep"},
		{"recursion through native map", `function f(){return [1].map(f)};f()`, ""},
		{"deep value by getter", `function mk(){var o={};Object.defineProperty(o,"a",{enumerable:true,get(){return mk()}});return o};JSON.stringify(mk())`, "too deep"},
		{"deep nested array toString", `var a=[];for(var i=0;i<300000;i++){a=[a]};String(a)`, ""},
		{"deep nested array flat", `var a=[];for(var i=0;i<300000;i++){a=[a]};a.flat(Infinity)`, ""},
		{"deep JSON.parse", `var s="";for(var i=0;i<20;i++){s=s+"[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[["};JSON.parse(s)`, "too deep"},
		{"huge return value", `var o={};for(var i=0;i<3000;i++){o={a:o}};return o`, ""},
		{"circular return", `var o={};o.o=o;return o`, "circular"},
		{"heap growth", `var a=[];for(;;){a.push("xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"+a.length)}`, "memory|too long|ran out of time"}, // whichever limit is reached first: the heap, the array length or the deadline
		{"catastrophic regex", `/^(a+)+\1b$/.test("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa");for(;;){}`, "ran out of time"},
		{"pending promise", `return new Promise(function(){})`, "never settles"},
		{"rejected promise", `return Promise.reject(new Error("no"))`, "no"},
		{"throw an object with a bad toString", `throw {toString(){throw 1}}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pkg := testPackage(t, simpleManifest, map[string]string{"main.js": `module.exports={rpc:{go(){` + c.code + `}}}`})
			r := NewRuntime(Options{Package: pkg, Target: TargetServer, HeapCap: 64 << 20})
			done := make(chan error, 1)
			go func() {
				_, err := call(t, r, 2*time.Second, nil)
				done <- err
			}()
			select {
			case err := <-done:
				if c.want != "" && (err == nil || !containsAny(err.Error(), c.want)) {
					t.Fatalf("got %v, want an error with %q", err, c.want)
				}
				if err != nil && strings.Contains(strings.ToLower(err.Error()), engineName) {
					t.Fatalf("the engine's name leaked: %v", err)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("the invocation did not come back")
			}
		})
	}
}

// Every builtin that could allocate from a small input or run long in one native call. A native
// call cannot be interrupted and a goroutine cannot be killed, so each of these has to stop by
// itself: none may be left running past the deadline, and the heap must stay near the cap.
func TestBuiltinsCannotAllocatePastTheCap(t *testing.T) {
	big := `var big="x".repeat(30000000);`
	sparse := `var a=[];a.length=4294967295;`
	cases := []struct {
		name, code string
		want       string // part of the error, "" for any end
	}{
		// the two reported
		{"Array.from array-like", `Array.from({length:4294967295})`, "too long"},
		{"split of a long string", `"x".repeat(67000000).split("")`, "too long"},
		{"split of 30M chars", big + `big.split("")`, "too long"},
		{"split with a limit", big + `big.split("",4294967295)`, "too long"},
		{"split with a regex", `"x".repeat(4000000).split(/(?:)/)`, ""},
		{"regex split over the input cap", big + `big.split(/x/)`, "too long"},
		{"Array.from with getter length", `var n=0;Array.from({get length(){return n++?4294967295:1}})`, "plain number"},
		{"length with valueOf", `Array.from({length:{valueOf(){return 4294967295}}})`, "plain number"},
		{"new Array then fill", `new Array(4000000000).fill(0)`, "too long"},
		{"Array call", `Array(4294967295).map(function(x){return x})`, "too long"},
		{"length set then fill", sparse + `a.fill(0)`, "too long"},
		{"fill through call", `[].fill.call({length:9007199254740991},0)`, "too long"},
		{"join with a long separator", `new Array(1000000).join("x".repeat(1000))`, "too long"},
		{"join of long elements", `var s="x".repeat(1000000);var a=[];for(var i=0;i<100;i++)a.push(s);a.join("")`, "too long"},
		{"toString of a sparse array", sparse + `String(a)`, "too long"},
		{"toLocaleString", sparse + `a.toLocaleString()`, "too long"},
		{"concat of a sparse array", sparse + `[].concat(a)`, "too long"},
		{"concat many", `var a=new Array(1000000);a.concat(a,a,a)`, "too long"},
		{"concat spreadable", `var o={length:4294967295};o[Symbol.isConcatSpreadable]=true;[].concat(o)`, "too long"},
		{"flat of a sparse array", sparse + `[a].flat()`, "too long"},
		{"sort", sparse + `a.sort()`, "too long"},
		{"reverse", sparse + `a.reverse()`, "too long"},
		{"slice", sparse + `a.slice()`, "too long"},
		{"keys loop is interruptible", sparse + `for(var k of a.keys()){}`, "ran out of time"},
		{"spread of a sparse array", sparse + `[...a]`, "ran out of time"},
		{"call spread of a sparse array", sparse + `Math.max(...a)`, "ran out of time"},
		{"destructuring rest", sparse + `var [x,...y]=a`, "ran out of time"},
		{"Map from a sparse array", sparse + `new Map(a.entries())`, "stopped"},
		{"Set from a sparse array", sparse + `new Set(a)`, ""},
		{"Array.from a sparse iterable", sparse + `Array.from(a)`, "ran out of time"},
		{"Object.fromEntries", sparse + `Object.fromEntries(a.entries())`, "stopped"},
		{"Promise.all", sparse + `Promise.all(a)`, ""},
		{"a promise loop", `(function f(){Promise.resolve().then(f)})();return 1`, ""},
		{"apply with a long array-like", `String.fromCharCode.apply(null,{length:4294967295})`, "too long"},
		{"apply with a sparse array", sparse + `Math.max.apply(null,a)`, "too long"},
		{"Reflect.apply", `Reflect.apply(String.fromCharCode,null,{length:4294967295})`, "too long"},
		{"Reflect.construct", `Reflect.construct(Date,{length:4294967295})`, "too long"},
		{"String.raw", `String.raw({raw:{length:4294967295}})`, "too long"},
		{"repeat", `"xxxx".repeat(1000000000)`, "too long"},
		{"padStart", `"x".padStart(4294967295)`, "too long"},
		{"padEnd", `"x".padEnd(4294967295,"abc")`, "too long"},
		{"string concat", big + `big.concat(big,big)`, "too long"},
		{"replaceAll with a long replacement", big + `big.replaceAll("x","xxxxxxxx")`, "too long"},
		{"replace all empty matches", `"x".repeat(1000000).replaceAll("","y".repeat(1000))`, "too long"},
		{"regex replace", `"x".repeat(1000000).replace(/(?:)/g,"y".repeat(1000))`, "too long"},
		{"replace with $ prefix", `"x".repeat(1000000).replace(/x/g,"$` + "`" + `")`, "too long"},
		{"replace with $&", `"x".repeat(1000000).replace(/x/g,"$&$&$&$&$&$&$&$&$&$&$&$&$&$&$&$&$&$&$&$&$&$&$&$&$&$&$&$&$&$&$&$&$&$&$&$&")`, "too long"},
		{"regex replace method", `/x/g[Symbol.replace]("x".repeat(1000000),"y".repeat(1000))`, "too long"},
		{"match over the input cap", big + `big.match(/x/g)`, "too long"},
		{"matchAll over the input cap", big + `big.matchAll(/x/g)`, "too long"},
		{"exec over the input cap", big + `/x/.exec(big)`, "too long"},
		{"a huge regex", `new RegExp("(".repeat(10000000))`, "too long"},
		{"normalize", big + `big.normalize()`, "too long"},
		{"encodeURIComponent", `var s="x".repeat(33000000);encodeURIComponent(s+s)`, "too long"},
		{"Object.keys of a long string", big + `Object.keys(big)`, "too long"},
		{"Object.values of a String", big + `Object.values(new String(big))`, "too long"},
		{"Object.entries", big + `Object.entries(big)`, "too long"},
		{"Object.getOwnPropertyNames", big + `Object.getOwnPropertyNames(big)`, "too long"},
		{"Object.assign from a string", big + `Object.assign({},big)`, "too long"},
		{"Reflect.ownKeys", big + `Reflect.ownKeys(new String(big))`, "too long"},
		{"string spread", big + `[...big]`, "ran out of time"},
		{"JSON.stringify many long strings", `var s="x".repeat(1000000);var a=[];for(var i=0;i<100;i++)a.push(s);JSON.stringify(a)`, "too long"},
		{"JSON.parse of a long string", `var s="x".repeat(33000000);JSON.parse(s+s)`, "too long"},
		{"typed arrays are gone", `new Uint8Array(4294967295)`, "not defined"},
		{"ArrayBuffer is gone", `new ArrayBuffer(4294967295)`, "not defined"},
		{"BigInt is gone", `BigInt(1)`, "not defined"},
		{"catastrophic regex", `/^(a+)+\1b$/.test("a".repeat(60));return 1`, ""},
		{"string doubling", `var s="x".repeat(30000000);for(;;){s+=s}`, ""},
		{"Map growth", `var m=new Map();for(var i=0;;i++){m.set(i,{})}`, ""},
		{"long sort of a million", `var a=[];for(var i=0;i<1000000;i++)a.push(i%7);a.sort();return a.length`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pkg := testPackage(t, simpleManifest, map[string]string{"main.js": `module.exports={rpc:{go(){` + c.code + `}}}`})
			r := NewRuntime(Options{Package: pkg, Target: TargetServer})
			defer r.Close()
			runtime.GC()
			base := heapNow()
			var peak atomic.Uint64
			stop := make(chan struct{})
			sampled := make(chan struct{})
			go func() {
				defer close(sampled)
				for {
					select {
					case <-stop:
						return
					case <-time.After(5 * time.Millisecond):
						if n := heapNow(); n > peak.Load() {
							peak.Store(n)
						}
					}
				}
			}()
			start := time.Now()
			_, err := r.Invoke(context.Background(), Invocation{Path: []string{"rpc", "go"}, Timeout: 2 * time.Second})
			took := time.Since(start)
			// what a native left running would keep allocating here
			time.Sleep(300 * time.Millisecond)
			close(stop)
			<-sampled
			if c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Errorf("got %v, want an error with %q", err, c.want)
			}
			for _, line := range r.Logs() {
				if strings.Contains(line.Text, "does not respond") {
					t.Errorf("a native call outlived the deadline: %s", line.Text)
				}
			}
			if took > 2*time.Second+interruptGrace {
				t.Errorf("took %s", took)
			}
			limit := int64(1500 << 20)
			if c.name == "string doubling" {
				// `+` is one instruction that cannot be checked first: the watchdog stops the
				// loop, a doubling or two past the cap. See "Known limitations" in docs/plugins.md.
				limit = 3 << 30
			}
			if grew := int64(peak.Load()) - int64(base); grew > limit {
				t.Errorf("the heap grew by %d MB", grew>>20)
			}
			runtime.GC()
		})
	}
}

// Ordinary code still works through the checks.
func TestChecksLeaveOrdinaryCodeAlone(t *testing.T) {
	cases := []struct{ expr, want string }{
		{`[1,2,3].map(function(x){return x*2}).join("-")`, "2-4-6"},
		{`[..."ab"].length+":"+[...[1,2]].length`, "2:2"},
		{`[..."a😀"].length`, "2"},
		{`Array.from({length:2},function(_,i){return i}).join()`, "0,1"},
		{`Array.from("abc").join("")`, "abc"},
		{`Array.from(new Set([1,1,2])).length`, "2"},
		{`new Map([["a",1]]).get("a")`, "1"},
		{`Math.max.apply(null,[1,5,3])`, "5"},
		{`Math.max(...[1,5,3])`, "5"},
		{`(function(){return Array.prototype.slice.call(arguments).length})(1,2,3)`, "3"},
		{`(function(a,...r){return r.length})(1,2,3)`, "2"},
		{`[1,[2,[3]]].flat(Infinity).length`, "3"},
		{`[[1],[2]].concat([3],4).length`, "4"},
		{`"a,b".split(",").length`, "2"},
		{`"a b".split(/ /).length`, "2"},
		{`"abc".split("",2).length`, "2"},
		{`"aXb".replace("X","$&$&")`, "aXXb"},
		{`"aXbX".replaceAll("X","y")`, "ayby"},
		{`"aXb".replace(/x/i,function(m){return m+m})`, "aXXb"},
		{`"a1b2".match(/\d/g).length`, "2"},
		{`[..."a1b2".matchAll(/\d/g)].length`, "2"},
		{`new RegExp("a","g").flags`, "g"},
		{`/a/ instanceof RegExp`, "true"},
		{`(/a/).constructor===RegExp`, "true"},
		{`"é".normalize("NFD").length`, "2"},
		{`"x".padStart(3,"-")`, "--x"},
		{`"ab".repeat(2)`, "abab"},
		{`Object.keys("ab").length`, "2"},
		{`Object.keys({a:1}).join()`, "a"},
		{`[1,2].toString()`, "1,2"},
		{`String([1,[2,3]])`, "1,2,3"},
		{`[null,undefined,1].join()`, ",,1"},
		{`String.raw` + "`a${1}b`", "a1b"},
		{`JSON.stringify({a:[1,"x",null,true],b:{c:1.5}})`, `{"a":[1,"x",null,true],"b":{"c":1.5}}`},
		{`JSON.stringify([1],null,1)`, "[\n 1\n]"},
		{`JSON.parse('{"a":[1]}').a[0]`, "1"},
		{`encodeURIComponent("a b")`, "a%20b"},
		{`(function(){var o=[];for(var k of [1,2].entries())o.push(k.join(":"));return o.join()})()`, "0:1,1:2"},
		{`(function(){var [a,b]=[1,2];var {c}={c:3};return a+b+c})()`, "6"},
		{`({...{a:1}}).a`, "1"},
	}
	for _, c := range cases {
		t.Run(c.expr, func(t *testing.T) {
			r := runner(t, `module.exports={rpc:{go(){return String(`+c.expr+`)}}}`)
			v, err := call(t, r, 5*time.Second, nil)
			if err != nil {
				t.Fatal(err)
			}
			if v != c.want {
				t.Fatalf("got %v, want %v", v, c.want)
			}
		})
	}
}

// A BigInt has no way in: arithmetic on one cannot be interrupted.
func TestBigIntLiteralsAreRefused(t *testing.T) {
	for _, code := range []string{`var x=2n`, `function f(){return 2n**(2n**40n)}`, `var o={a:[1n<<1000000000000n]}`} {
		r := runner(t, code+`;module.exports={rpc:{go(){return "ran"}}}`)
		if _, err := call(t, r, 5*time.Second, nil); err == nil || !strings.Contains(err.Error(), "BigInt") {
			t.Errorf("%s: got %v", code, err)
		}
	}
	r := runner(t, `var s="2n";/* 2n */module.exports={rpc:{go(){return s}}}`)
	if v, err := call(t, r, 5*time.Second, nil); err != nil || v != "2n" {
		t.Errorf("in a string it is only text: %v %v", v, err)
	}
}

// Progress goes to every browser watching, so a loop cannot flood them.
func TestProgressIsRateLimited(t *testing.T) {
	r := runner(t, `module.exports={rpc:{go(){for(var i=0;i<20000;i++)lucode.context.progress("n"+i);return 1}}}`)
	n := 0
	_, err := r.Invoke(context.Background(), Invocation{Path: []string{"rpc", "go"}, Timeout: 5 * time.Second, Progress: func(string) { n++ }})
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 || n > 25 {
		t.Fatalf("%d progress messages", n)
	}
}

func TestErrorsAreScrubbed(t *testing.T) {
	if got := Scrub("panic in github.com/dop251/" + engineName + "/runtime.go:12 " + strings.ToUpper(engineName)); strings.Contains(strings.ToLower(got), engineName) || strings.Contains(got, ".go:") {
		t.Fatalf("got %q", got)
	}
	r := runner(t, `module.exports={rpc:{go(){ null.x }}}`)
	_, err := call(t, r, time.Second*5, nil)
	var pe *Error
	if !errors.As(err, &pe) || strings.Contains(strings.ToLower(pe.Msg), engineName) {
		t.Fatalf("got %T %v", err, err)
	}
}

// Nothing a host gives a script is a wrapper around a Go value: everything is a plain copy.
func TestValuesCrossAsPlainJSON(t *testing.T) {
	r := runner(t, `module.exports={rpc:{go(a){
		a.list.push(4); a.map.added=true;
		return {
			arr: Array.isArray(a.list),
			proto: Object.getPrototypeOf(a.map)===Object.prototype,
			fn: typeof lucode.log.info,
			date: new Date(0).toISOString(),
			undef: undefined,
			back: a,
		}
	}}}`)
	arg := map[string]any{"list": []any{1.0, 2.0}, "map": map[string]any{}}
	v, err := call(t, r, 5*time.Second, arg)
	if err != nil {
		t.Fatal(err)
	}
	m := v.(map[string]any)
	if m["arr"] != true || m["proto"] != true || m["fn"] != "function" {
		t.Fatalf("got %+v", m)
	}
	if _, has := m["undef"]; has {
		t.Fatalf("undefined should not cross: %+v", m)
	}
	if len(arg["list"].([]any)) != 2 || len(arg["map"].(map[string]any)) != 0 {
		t.Fatalf("the script reached the host's own values: %+v", arg)
	}
}

// No state survives between invocations except in lucode.memory.
func TestFreshVMPerInvocation(t *testing.T) {
	r := runner(t, `var n=0;globalThis.g=(globalThis.g||0)+1;module.exports={rpc:{go(){
		n++;
		var m=(lucode.memory.get("m")||0)+1; lucode.memory.set("m",m);
		return {n:n,g:globalThis.g,m:m}
	}}}`)
	for i := 1; i <= 3; i++ {
		v, err := call(t, r, 5*time.Second, nil)
		if err != nil {
			t.Fatal(err)
		}
		m := v.(map[string]any)
		if m["n"] != 1.0 || m["g"] != 1.0 || m["m"] != float64(i) {
			t.Fatalf("round %d: %+v", i, m)
		}
	}
}

func TestRequireStaysInTheZip(t *testing.T) {
	pkg := testPackage(t, simpleManifest, map[string]string{
		"main.js":       `module.exports={rpc:{go(a){return require(a.path)}}}`,
		"lib/x.js":      `exports.v=require("./y").v+1`,
		"lib/y.js":      `exports.v=41`,
		"lib/data.json": `{"a":1}`,
	})
	outside := filepath.Join(filepath.Dir(pkg.Dir), "outside.js")
	os.WriteFile(outside, []byte(`exports.v=1`), 0o644)
	r := NewRuntime(Options{Package: pkg, Target: TargetServer})
	v, err := call(t, r, 5*time.Second, map[string]any{"path": "./lib/x.js"})
	if err != nil || v.(map[string]any)["v"] != 42.0 {
		t.Fatalf("relative require: %v %v", v, err)
	}
	if v, err := call(t, r, 5*time.Second, map[string]any{"path": "./lib/data.json"}); err != nil || v.(map[string]any)["a"] != 1.0 {
		t.Fatalf("json require: %v %v", v, err)
	}
	for _, bad := range []string{"fs", "os", "../outside.js", "./../../outside.js", "/etc/passwd", `C:\Windows\win.ini`, "./plugin.json/../../x.js", "./lib/nope.js"} {
		if _, err := call(t, r, 5*time.Second, map[string]any{"path": bad}); err == nil {
			t.Errorf("require(%q) should fail", bad)
		}
	}
}

// Host functions of the other target throw a clear error, on both targets.
func TestOtherTargetThrowsClearly(t *testing.T) {
	manifest := `{"id":"acme.both","name":"B","version":"1.0.0","sdk":1,"targets":{"server":{"main":"s.js"},"runner":{"main":"r.js"}}}`
	code := `module.exports={rpc:{go(a){try{ lucode[a.mod][a.fn]("x"); return "no error" }catch(e){ return String(e.message) }}}}`
	pkg := testPackage(t, manifest, map[string]string{"s.js": code, "r.js": code})
	cases := []struct{ target, mod, fn, want string }{
		{TargetServer, "fs", "read", "only works in the runner target"},
		{TargetServer, "git", "status", "only works in the runner target"},
		{TargetServer, "exec", "run", "only works in the runner target"},
		{TargetServer, "server", "call", "only works in the runner target"},
		{TargetRunner, "settings", "get", "only works in the server target"},
		{TargetRunner, "storage", "get", "only works in the server target"},
		{TargetRunner, "board", "listLists", "only works in the server target"},
		{TargetRunner, "browser", "launch", "only works in the server target"},
		{TargetRunner, "conversations", "list", "only works in the server target"},
		{TargetRunner, "sessions", "get", "only works in the server target"},
		{TargetRunner, "flows", "emit", "only works in the server target"},
		{TargetRunner, "runners", "list", "only works in the server target"},
		{TargetRunner, "relay", "status", "only works in the server target"},
		{TargetRunner, "relay", "createEndpoint", "only works in the server target"},
	}
	for _, c := range cases {
		t.Run(c.target+"/"+c.mod, func(t *testing.T) {
			r := NewRuntime(Options{Package: pkg, Target: c.target})
			v, err := call(t, r, 5*time.Second, map[string]any{"mod": c.mod, "fn": c.fn})
			if err != nil {
				t.Fatal(err)
			}
			if s, _ := v.(string); !strings.Contains(s, c.want) {
				t.Fatalf("got %v", v)
			}
		})
	}
}

// Cancelling interrupts a VM and a blocking host call alike, and a stopped plugin's slot frees up.
func TestCancelAndConcurrencyCap(t *testing.T) {
	r := runner(t, `module.exports={rpc:{go(a){ lucode.time.sleep(a.ms); return "done" }}}`)
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		_, err := r.Invoke(ctx, Invocation{Path: []string{"rpc", "go"}, Timeout: time.Minute, Arg: map[string]any{"ms": 60000}})
		errs <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-errs:
		if err == nil {
			t.Fatal("expected a stop")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not interrupt a sleep")
	}

	// more than the cap: the extra ones wait in line, and give up on their own deadline
	for i := 0; i < maxConcurrent; i++ {
		go r.Invoke(context.Background(), Invocation{Path: []string{"rpc", "go"}, Timeout: 2 * time.Second, Arg: map[string]any{"ms": 1500}})
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := r.Invoke(context.Background(), Invocation{Path: []string{"rpc", "go"}, Timeout: 100 * time.Millisecond, Arg: map[string]any{"ms": 1}}); err == nil || !strings.Contains(err.Error(), "waiting") {
		t.Fatalf("expected to time out in the queue, got %v", err)
	}
	r.Close()
	if _, err := call(t, r, time.Second, map[string]any{"ms": 1}); err == nil {
		t.Fatal("a closed plugin runs nothing")
	}
}

func TestLocksAndHandlers(t *testing.T) {
	r := runner(t, `module.exports={rpc:{
		nested(){ return lucode.locks.with("a",function(){ return lucode.locks.with("a",function(){return 1}) }) },
		ok(){ return lucode.locks.with("a",function(){ return 7 }) },
		thrown(){ try{ lucode.locks.with("a",function(){ throw new Error("inside") }) }catch(e){ return e.message } },
	}}`)
	if _, err := r.Invoke(context.Background(), Invocation{Path: []string{"rpc", "nested"}, Timeout: time.Second * 2}); err == nil || !strings.Contains(err.Error(), "already holds") {
		t.Fatalf("nested: %v", err)
	}
	if v, err := r.Invoke(context.Background(), Invocation{Path: []string{"rpc", "thrown"}, Timeout: time.Second * 2}); err != nil || v != "inside" {
		t.Fatalf("thrown: %v %v", v, err)
	}
	if v, err := r.Invoke(context.Background(), Invocation{Path: []string{"rpc", "ok"}, Timeout: time.Second * 2}); err != nil || v != 7.0 {
		t.Fatalf("after a throw the lock is free: %v %v", v, err)
	}
	if _, err := r.Invoke(context.Background(), Invocation{Path: []string{"rpc", "nope"}, Timeout: time.Second}); !errors.Is(err, ErrNoHandler) {
		t.Fatalf("missing handler: %v", err)
	}
}

// containsAny reports whether s contains any of the "|"-separated alternatives in want.
func containsAny(s, want string) bool {
	for _, w := range strings.Split(want, "|") {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// OnLog hears each line lucode.log writes, scrubbed and cut like the kept one.
func TestOnLogHearsEachLine(t *testing.T) {
	pkg := testPackage(t, simpleManifest, map[string]string{"main.js": `module.exports={rpc:{go(){lucode.log.info("hi", {a:1}); lucode.log.error("bad")}}}`})
	var heard []LogEntry
	r := NewRuntime(Options{Package: pkg, Target: TargetServer, OnLog: func(e LogEntry) { heard = append(heard, e) }})
	if _, err := call(t, r, 2*time.Second, nil); err != nil {
		t.Fatal(err)
	}
	if len(heard) != 2 || heard[0].Level != "info" || heard[0].Text != `hi {"a":1}` || heard[1].Level != "error" || heard[1].Text != "bad" {
		t.Fatalf("heard %+v", heard)
	}
	if kept := r.Logs(); len(kept) != 2 || kept[0] != heard[0] {
		t.Fatalf("kept %+v", kept)
	}
}
