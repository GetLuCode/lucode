package plugins

import (
	"strings"
	"testing"
)

func pageManifest(pages, extra string) string {
	return `{"id":"acme.x","name":"X","version":"1.0.0","sdk":1,"targets":{"server":{"main":"dist/server.js"}},` +
		`"contributes":{"pages":[` + pages + `]}` + extra + `}`
}

func TestFrontendPageRules(t *testing.T) {
	cases := []struct {
		name, pages, extra, want string
	}{
		{"view tree", `{"id":"main","label":"M","render":"renderMain"}`, ``, ``},
		{"frontend", `{"id":"main","label":"M","ui":"dist/main.js","rpc":["list","add"]}`, ``, ``},
		{"frontend with no rpc", `{"id":"main","label":"M","ui":"dist/main.js"}`, ``, ``},
		{"both", `{"id":"main","label":"M","render":"r","ui":"dist/main.js"}`, ``, `not both`},
		{"neither", `{"id":"main","label":"M"}`, ``, `needs "render"`},
		{"ui not js", `{"id":"main","label":"M","ui":"dist/main.html"}`, ``, `must be a .js file`},
		{"ui outside", `{"id":"main","label":"M","ui":"../main.js"}`, ``, `must be a .js file`},
		{"bad rpc name", `{"id":"main","label":"M","ui":"dist/main.js","rpc":["a-b"]}`, ``, `not an rpc name`},
		{"rpc twice", `{"id":"main","label":"M","ui":"dist/main.js","rpc":["a","a"]}`, ``, `twice`},
		{"rpc on a view tree", `{"id":"main","label":"M","render":"r","rpc":["a"]}`, ``, `is for a page with "ui"`},
		{"build", `{"id":"main","label":"M","ui":"dist/main.js"}`, `,"build":{"server":"src/server.ts","pages":{"main":"src/ui/main.tsx"}}`, ``},
		{"build of a missing page", `{"id":"main","label":"M","ui":"dist/main.js"}`, `,"build":{"pages":{"other":"src/o.tsx"}}`, `not a page with "ui"`},
		{"build of a view tree", `{"id":"main","label":"M","render":"r"}`, `,"build":{"pages":{"main":"src/o.tsx"}}`, `not a page with "ui"`},
		{"build outside", `{"id":"main","label":"M","ui":"dist/main.js"}`, `,"build":{"server":"../x.ts"}`, `inside the plugin's folder`},
		{"build with no runner", `{"id":"main","label":"M","ui":"dist/main.js"}`, `,"build":{"runner":"src/r.ts"}`, `needs targets.runner`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(pageManifest(c.pages, c.extra)), ParseOptions{})
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("got %v, want an error with %q", err, c.want)
			}
		})
	}
	many := make([]string, MaxPageRPCs+1)
	for i := range many {
		many[i] = `"r` + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + `"`
	}
	_, err := ParseManifest([]byte(pageManifest(`{"id":"main","label":"M","ui":"dist/main.js","rpc":[`+strings.Join(many, ",")+`]}`, ``)), ParseOptions{})
	if err == nil || !strings.Contains(err.Error(), "is the most") {
		t.Fatalf("too many rpcs: %v", err)
	}
}

func TestFrontendPageMustBeInTheZip(t *testing.T) {
	m := pageManifest(`{"id":"main","label":"M","ui":"dist/main.js","rpc":["a"]}`, ``)
	_, err := CheckZip(writeZip(t, []zipEntry{{name: "plugin.json", body: m}, {name: "dist/server.js", body: "module.exports={}"}}), ParseOptions{})
	if err == nil || !strings.Contains(err.Error(), `"dist/main.js", which is not in the zip`) {
		t.Fatalf("a missing frontend: %v", err)
	}
	got, err := CheckZip(writeZip(t, []zipEntry{{name: "plugin.json", body: m}, {name: "dist/server.js", body: "module.exports={}"}, {name: "dist/main.js", body: "1"}}), ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if p := got.Contributes.Pages[0]; p.Style() != "dist/main.css" || p.RPC[0] != "a" {
		t.Fatalf("page: %+v %s", p, p.Style())
	}
}
