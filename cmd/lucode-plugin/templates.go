package main

import (
	"claude-orchestrator/internal/plugins/compile"
	"claude-orchestrator/sdk"
)

// the files of a new plugin with a backend and a Preact page
func frontendTemplate() map[string]string {
	return map[string]string{
		"plugin.json":              uiManifest,
		"src/server.ts":            uiServer,
		"src/ui/main.tsx":          uiMain,
		"src/ui/style.css":         uiStyle,
		"test/main.test.ts":        uiTest,
		"assets/icon.svg":          newIcon,
		"tsconfig.json":            uiTSConfig,
		"package.json":             uiPackage,
		"types/lucode-plugin.d.ts": sdk.PluginTypes,
		"types/lucode-ui.d.ts":     sdk.UITypes,
		"types/lucode-test.d.ts":   sdk.TestTypes,
		"README.md":                uiReadme,
		".gitignore":               "dist/\nnode_modules/\n*.zip\n",
	}
}

const uiManifest = `{
  "id": "__ID__",
  "name": "__NAME__",
  "version": "0.1.0",
  "sdk": 1,
  "description": "What it does, in one sentence.",
  "icon": "assets/icon.svg",
  "targets": {
    "server": { "main": "dist/server.js", "permissions": { "storage": true } }
  },
  "contributes": {
    "pages": [
      { "id": "main", "label": "__NAME__", "icon": "assets/icon.svg", "ui": "dist/main.js", "rpc": ["listItems", "addItem"] }
    ]
  },
  "build": {
    "server": "src/server.ts",
    "pages": { "main": "src/ui/main.tsx" }
  }
}
`

const uiServer = `// The backend: it runs on the hub, in the plugin runtime. The page calls these rpcs, and only the
// ones plugin.json lists in the page's "rpc".

interface Item {
  text: string;
  at: string;
}

const sdk = lucode as Lucode.ServerSDK;
const keyOf = (projectId: string) => "items/" + projectId;
const read = (projectId: string): Item[] => sdk.storage.get<Item[]>(keyOf(projectId)) || [];

export const rpc = {
  listItems(args: Lucode.RpcArgs) {
    return { items: read(String(args.projectId)) };
  },

  // Two people can add at once: the read and the write go together.
  addItem(args: Lucode.RpcArgs) {
    const text = String(args.text || "").trim();
    if (!text) throw new Error("An item needs some text.");
    const projectId = String(args.projectId);
    const items = sdk.locks.with("items", () => {
      const next = read(projectId).concat([{ text, at: new Date().toISOString() }]);
      sdk.storage.set(keyOf(projectId), next);
      return next;
    });
    return { items };
  },
};
`

const uiMain = `// The page: Preact in a Worker of the browser. document.body is the page's area in lucode.
import { render } from "preact";
import { useEffect, useState } from "preact/hooks";
import { context, rpc, toast } from "@lucode/ui";
import "./style.css";

interface Item {
  text: string;
  at: string;
}

function App() {
  const [items, setItems] = useState<Item[] | null>(null);
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    rpc<{ items: Item[] }>("listItems")
      .then(r => setItems(r.items))
      .catch(e => setError(e.message));
  }, []);

  async function add(e: Event) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const r = await rpc<{ items: Item[] }>("addItem", { text });
      setItems(r.items);
      setText("");
      toast("Added");
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <main class="app">
      <h1>{context.pluginName}</h1>
      <form class="add" onSubmit={add}>
        <input
          value={text}
          onInput={e => setText(e.currentTarget.value)}
          placeholder="Something to remember"
          aria-label="New item"
        />
        <button type="submit" disabled={busy || !text.trim()}>Add</button>
      </form>
      {error && <p class="error" role="alert">{error}</p>}
      {items === null ? (
        <p class="muted">Loading…</p>
      ) : items.length ? (
        <ul class="items">
          {items.map(item => <li key={item.at}>{item.text}</li>)}
        </ul>
      ) : (
        <p class="muted">Nothing yet.</p>
      )}
    </main>
  );
}

render(<App />, document.body);
`

const uiStyle = `/* The page's own styles: they reach nothing outside it. The app's theme comes in as variables
   (--lc-bg, --lc-surface, --lc-text, --lc-muted, --lc-accent, --lc-border, --lc-danger), so the
   page follows dark and light without doing anything. url() only takes data: and #ids. */

.app {
  display: grid;
  gap: 16px;
  max-width: 640px;
}

h1 {
  margin: 0;
  font-size: 20px;
}

.add {
  display: flex;
  gap: 8px;
}

input {
  flex: 1;
  padding: 8px 10px;
  border: 1px solid var(--lc-border);
  border-radius: 8px;
  background: var(--lc-surface);
  color: var(--lc-text);
  font: inherit;
}

button {
  padding: 8px 14px;
  border: 0;
  border-radius: 8px;
  background: var(--lc-accent);
  color: var(--lc-on-accent);
  font: inherit;
  cursor: pointer;
}

button:disabled {
  opacity: 0.5;
  cursor: default;
}

.items {
  margin: 0;
  padding: 0;
  list-style: none;
  display: grid;
  gap: 6px;
}

.items li {
  padding: 10px 12px;
  border: 1px solid var(--lc-border);
  border-radius: 8px;
  background: var(--lc-surface);
}

.muted {
  color: var(--lc-muted);
}

.error {
  color: var(--lc-danger);
}
`

const uiTest = `// lucode-plugin test runs this: the backend in the plugin runtime, the page in the browser's page
// runtime, and nothing leaves this machine.

test("adds an item from the page", () => {
  const page = plugin.page("main");
  assert.includes(page.text(), "Nothing yet.");
  assert.ok(page.disabled("text=Add"), "the button waits for some text");

  page.input("input", "Buy milk");
  page.click("text=Add");

  assert.equal(page.text("ul"), "Buy milk");
  assert.deepEqual(page.toasts(), [{ message: "Added", tone: "default" }]);
  assert.deepEqual(plugin.rpc("listItems").items.map((i: { text: string }) => i.text), ["Buy milk"]);
});

test("refuses an item without text", () => {
  assert.throws(() => plugin.rpc("addItem", { text: "  " }), "needs some text");
});

test("each project has its own items", () => {
  plugin.rpc("addItem", { text: "Only here" }, { projectId: "p1" });
  assert.equal(plugin.rpc("listItems", {}, { projectId: "p2" }).items.length, 0);
});
`

const uiTSConfig = `{
  "compilerOptions": {
    "target": "ES2020",
    "module": "ESNext",
    "moduleResolution": "Bundler",
    "jsx": "react-jsx",
    "jsxImportSource": "preact",
    "strict": true,
    "noEmit": true,
    "skipLibCheck": true,
    "lib": ["ES2020", "DOM"]
  },
  "include": ["src", "test", "types"]
}
`

var uiPackage = `{
  "private": true,
  "description": "Only for your editor's types: lucode-plugin build has Preact ` + compile.PreactVersion + ` and needs no node_modules.",
  "devDependencies": {
    "preact": "` + compile.PreactVersion + `",
    "typescript": "^5.9.0"
  }
}
`

const uiReadme = "# __NAME__\n\n" +
	"A lucode plugin with a backend (`src/server.ts`, on the hub) and a page in Preact (`src/ui/main.tsx`, in the browser).\n\n" +
	"```\n" +
	"lucode-plugin test .     # builds, then runs test/*.test.ts without a browser and without lucode\n" +
	"lucode-plugin pack .     # builds, checks and writes the zip to install from Settings, Plugins\n" +
	"```\n\n" +
	"`npm install` is only for your editor's types. The page can only call the rpcs that `plugin.json`\n" +
	"lists in the page's `\"rpc\"`. How plugins work: docs/plugin-sdk.md in the lucode repository.\n"
