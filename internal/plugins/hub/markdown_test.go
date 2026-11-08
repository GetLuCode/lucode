package hub_test

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"claude-orchestrator/internal/cdp"
	"claude-orchestrator/internal/plugins/bundled"
)

// A page shaped like Google's AI Mode answer (the data attributes, the role=heading divs,
// the language label above the code, the real link address in a comment, a hidden feedback
// form, source pills). It checks that markdown.js turns it into Markdown, offline.
const fakeAnswer = `<!doctype html><body>
<div data-scope-id="turn"><div data-container-id="main-col">
 <div role="heading" aria-level="3">Elementos <!--x--></div>
 <h4>Una sección</h4>
 <div role="heading" aria-level="5">Un detalle</div>
 <h6>Una nota</h6>
 <div style="display:block">Un párrafo con <strong>negritas</strong> y <em>cursiva</em>, y un enlace a
   <div style="display:inline"><a href="/goto?url=CAESabc">Wikipedia<!--TgQPHd|||[[&quot;https://es.wikipedia.org/wiki/A?x=1&amp;y=2&quot;,null,[1]]]--></a></div>.
   <button>Copiar</button></div>
 <ul><li><span>Uno</span></li><li><span>Dos</span><button>x</button></li><li><button>solo un botón</button></li></ul>
 <ol><li>Primero</li><li>Segundo</li></ol>
 <div><div><div>python</div></div><div><pre><code>def f(x):
    return x</code></pre></div></div>
 <div>Usa el código con precaución.</div>
 <table><tbody><tr><th>A</th><th>B | C</th></tr><tr><td>1</td><td>2</td></tr></tbody></table>
 <div style="display:none">Gracias por hacérnoslo saber</div>
 <div>Referencias:</div>
 <ul><li><span>Sitio oficial: </span><div style="display:inline"><span><a href="/goto?url=CAESdef">python.org<!--TgQPHd|||[[&quot;http://python.org&quot;,null,[1]]]--></a></span></div></li></ul>
 <div>La IA puede cometer errores, así que verifica las respuestas</div>
</div></div></body>`

func TestMarkdownConversion(t *testing.T) {
	if _, err := cdp.FindEdge(); err != nil {
		t.Skip("Edge is not installed")
	}
	raw, err := bundled.FS.ReadFile("gemini/data/markdown.js")
	if err != nil {
		t.Fatal(err)
	}
	answerJS := string(raw)
	t.Setenv("LUCODE_EDGE_STATE", filepath.Join(t.TempDir(), "state.json"))
	b := cdp.New("aimode-md", 9477)
	if _, err := b.Launch(cdp.LaunchOptions{Headless: true, Port: 9477, ProfileDir: t.TempDir(), URL: "data:text/html;charset=utf-8," + url.PathEscape(fakeAnswer)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	var got string
	for i := 0; i < 50; i++ { // the page was opened by Launch without waiting for it to load
		if got, err = b.Eval(answerJS); err == nil && strings.Contains(got, "Referencias:") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	// Google's h4 and h5 are shown as h2 and h3; the other levels stay
	want := "### Elementos\n\n" +
		"## Una sección\n\n" +
		"### Un detalle\n\n" +
		"###### Una nota\n\n" +
		"Un párrafo con **negritas** y *cursiva*, y un enlace a [Wikipedia](https://es.wikipedia.org/wiki/A?x=1&y=2).\n\n" +
		"- Uno\n- Dos\n\n" +
		"1. Primero\n2. Segundo\n\n" +
		"```python\ndef f(x):\n    return x\n```\n\n" +
		"| A | B \\| C |\n| --- | --- |\n| 1 | 2 |\n\n" +
		"Referencias:\n\n" +
		"- Sitio oficial: [python.org](http://python.org)"
	// the plugin's clean() takes the page's closing notice off; that part has its own test
	cleaned := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(got), "La IA puede cometer errores, así que verifica las respuestas"))
	if cleaned != want {
		t.Errorf("markdown:\n%q\n--- want:\n%q", cleaned, want)
	}
}
