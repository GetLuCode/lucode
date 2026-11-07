# Writing a plugin

How to write a lucode plugin. To install and run them, see `docs/plugins.md`.

In `sdk/`: `lucode-plugin.d.ts` (types), `plugin.schema.json` (the manifest) and `template/` (a minimal
plugin to copy). `internal/plugins/bundled/board-digest` and `webhooks` are two complete examples.

## What you are writing

Plain CommonJS JavaScript, in one or two entry files, that exports functions. lucode calls them. A fresh VM
runs each call, so nothing you put in a variable survives to the next one: keep state with `lucode.memory` or
`lucode.storage`. Handlers are synchronous. There are no timers, no `process`, no `fetch`, no `URL`, no
`TextEncoder`: what you get is standard JavaScript plus the `lucode` global.

## Zip layout

```
plugin.json            required, at the root
server.js              the server entry, as named in the manifest
runner.js              the runner entry
lib/x.js               require('./lib/x.js'), inside the zip only
data/**                read-only data, through lucode.data
blueprints/*.json      flow graphs
assets/**              icons: svg, png, webp, jpg
```

Only these extensions are allowed: `.json .js .md .txt .html .css .svg .png .webp .jpg .jpeg`. HTML and CSS are
inert data, never served to a page. The zip is refused for: more than 50 MB, 200 MB unpacked, 2000 files or 20 MB
in a file; a suspicious compression ratio; paths with `..`, a leading `/`, a backslash, a colon, a NUL, or a name
Windows reserves; symlinks; names that differ only by case.

## The command line

`cmd/lucode-plugin` uses the validation the hub uses, from the same package, so it cannot disagree with an upload.

```
go build -o lucode-plugin ./cmd/lucode-plugin

lucode-plugin new acme.hello              # starts a plugin in ./hello
lucode-plugin validate hello              # a folder or a zip
lucode-plugin pack hello -o hello.zip     # validates, then writes it (default <id>-<version>.zip)
```

`validate` and `pack` run everything the hub checks on an upload, then two things the hub would only tell you after
installing: entry files that do not compile, and blueprints that would not publish. They list the permissions the review
dialog will show. Your code is loaded in the sandbox to compile it and no handler is called, but its top level does run.
Names starting with `.` or `_` and `*.d.ts` files stay out of the zip, so tests can live in a `_test` folder. `-bundled`
allows a `lucode.*` id, for the plugins in this repository.

## Manifest

```jsonc
{
  "id": "acme.hello",            // ^[a-z0-9]+(\.[a-z0-9-]+)+$, lucode.* is reserved
  "name": "Hello",
  "version": "1.2.0",            // semver
  "sdk": 1,
  "description": "...", "author": "...", "homepage": "https://...",
  "icon": "assets/icon.svg",
  "targets": {
    "server": { "main": "server.js", "permissions": { "storage": true } },
    "runner": { "main": "runner.js", "permissions": { "fs": "read" } }
  },
  "contributes": { /* below */ }
}
```

A plugin has one target or both. The old form, `"main"` and `"permissions"` at the top, still means the server
target; giving both forms for the server is an error. **Fields the hub does not know are an error**, so nothing a
plugin asks for goes unseen. That includes `$schema`: point your editor at the schema in its settings instead.

### Permissions

| Permission | Target | Key in a grant | Risk | Gives |
|---|---|---|---|---|
| `storage: true` | server | `server.storage` | low | `lucode.storage` |
| `conversations: true` | server | `server.conversations` | low | `lucode.conversations` |
| `sessions: "read"` | server | `server.sessions:read` | medium | `lucode.sessions` |
| `board: "read"` | server | `server.board:read` | low | reads of `lucode.board` |
| `board: "write"` | server | also `server.board:write` | medium | writes of `lucode.board` |
| `flows: true` | server | `server.flows` | medium | `lucode.flows.emit`, and contributing `flowNodes` and `blueprints` |
| `browser: {origins: [...]}` | server | `server.browser:<origin>` | high | `lucode.browser`, each origin https |
| `relay: {providers: [...]}` | server | `server.relay:<provider>` | medium | `lucode.relay`, and the `webhook` event, per provider (`telegram`, ...) |
| `http: {hosts: [...]}` | both | `<target>.http:<host>` | medium | `lucode.http`, per target |
| `fs: "read"` | runner | `runner.fs:read` | medium | reads of `lucode.fs` |
| `fs: "write"` | runner | also `runner.fs:write` | high | `lucode.fs.write` |
| `git: "read"` | runner | `runner.git:read` | medium | `lucode.git` |
| `exec: {commands: [...]}` | runner | `runner.exec:<command>` | high | `lucode.exec`, base names only |

Hosts, origins, commands and relay providers each have their own key, so an upgrade that adds one needs a new grant. A host is a
name, `*.example.org` matches names under it and never `example.org` itself, and addresses, ports and a bare `*`
are refused. **Ask for the least that works**: every line is one more thing an admin has to accept.

### Contributions

```jsonc
"contributes": {
  "models": [{ "id": "mine", "label": "My model", "mode": "Mine", "hint": "Shown under the mode",
               "icon": "assets/m.svg", "accent": "#4285f4", "images": true, "loader": "skeleton",
               "waitingText": "Waiting…", "timeoutSeconds": 720 }],
  "conversations": [{ "id": "mine", "model": "mine", "title": "My conversations",
                      "emptyText": "None yet.", "showUrl": true }],   // needs conversations
  "pages": [{ "id": "main", "label": "Hello", "icon": "assets/i.svg", "render": "renderMain" }],
  "settings": [{ "key": "primer", "label": "Primer", "type": "textarea", "default": "", "help": "..." }],
  "blueprints": [{ "key": "ask", "name": "Ask", "description": "...", "file": "blueprints/ask.json" }],
  "flowNodes": [{ "id": "ask", "label": "Ask", "description": "...", "icon": "assets/x.svg", "target": "server",
                  "fields": [{ "name": "question", "label": "Question", "kind": "template", "required": true }],
                  "ports": ["next"], "timeoutSeconds": 600 }],
  "chatActions": [{ "id": "sum", "label": "Summarize", "icon": "assets/x.svg", "rpc": "summarize" }],
  "cardActions": [{ "id": "research", "label": "Research", "icon": "assets/x.svg", "rpc": "research", "target": "server" }]
}
```

Rules the hub checks:

- Models and pages need a server target. Settings too: they never reach a runner by themselves.
- A model's id cannot start with `claude`, `sonnet`, `opus`, `haiku`, `fable`, `anthropic`, `ollama` or `local`. On
  the wire it becomes `plugin:<pluginId>/<modelId>`: the plugin's id comes from the host, so a manifest cannot claim
  another plugin's model.
- **A flow node needs the `flows` permission of the server target, even if it runs on a runner.** So a plugin with a
  runner node has both targets.
- **Name a node's ports.** `failed` is always added; with no `ports` given, that is the only one the node has.
- `flowNodes[].target`, `chatActions[].target` and `cardActions[].target` default to `server`.
- `settings[].type`: `text`, `textarea`, `number`, `bool`, `select`, `secret`. `options` is a list of strings.
- `fields[].kind`: `text`, `template`, `expr`, `number`, `select`, `agent`, `list`, `bool`, `lines`, as in base nodes.
- `timeoutSeconds` is at most 900.
- Model `loader`: `"skeleton"` draws skeleton lines with your icon and `waitingText`. Anything else draws the plain dots.

## Exports

```js
module.exports = {
  models: { mine: { turn(req) { return "the answer, as markdown"; } } },
  nodes:  { ask(input) { return { port: "next", output: { answer: "..." } }; } },
  rpc:    { renderMain(args) { return { type: "text", text: "Hello" }; }, summarize(args) { return { toast: "Done" }; } },
  events: { installed(e) {}, upgraded(e) {}, uninstalling(e) {}, sessionDeleted(e) {}, sessionClosed(e) {}, cardMoved(e) {} },
};
```

The runner entry exports only `nodes` and `rpc`.

- **`models.<id>.turn(req)`**: `req` is `{sessionId, projectId, message, image: {base64, mediaType} | null, model}`. It
  must return text.
- **`nodes.<id>(input)`**: `input` is `{config, card, event, nodes, run, projectId}`. Return `{port, output}`. Without a
  port it takes the first one you declared. Only a declared port can be taken. `output` is at most 256 KB, and later
  nodes read it as `nodes.<nodeId>.<field>`. **Throw on failure**: that takes `failed`, or fails the run if `failed` is
  not connected. Returning `{port: "failed"}` with nothing connected there ends the path without an error.
- **`rpc.<name>(args)`**: called from pages, actions and `lucode.runners.call`. `args` includes `projectId` and the ids
  given. The browser can only call an rpc named by a contribution, or offered as an action in a view tree this plugin
  already returned, so an rpc nothing names is not reachable from a browser.
- **`events`** run in the background with a 10 s deadline, on the server target only, and only while the plugin is
  enabled. A plugin that throws here only gets a log line.

  | Event | When | `e` |
  |---|---|---|
  | `installed` | After this plugin is installed. No project in `lucode.context`. | `{version}` |
  | `upgraded` | After a new version replaces the old one. No project in `lucode.context`. | `{version}` |
  | `uninstalling` | Before this plugin is removed. The removal waits for it, up to the deadline. | `{}` |
  | `sessionDeleted` | A chat of the project was deleted. Goes to every enabled plugin, with a conversation in it or not. | `{sessionId, projectId}` |
  | `sessionClosed` | A chat this plugin has a conversation in is over: it was deleted, or a new Supervisor conversation replaced it. A replaced chat still exists and can come back. Only goes to plugins with a conversation in it. | `{sessionId, projectId}` |
  | `cardMoved` | A card changed list. Goes to every enabled plugin with the `board` permission; stops past the flows' depth limit. | `{cardId, projectId, from_list, from_kind, to_list, to_kind}` |
  | `webhook` | A service posted to an address this plugin got from `lucode.relay.createEndpoint`. Only the owner of the address gets it, if it is enabled and has the `relay` permission for that provider. Runs in the address's project. See "Webhooks". | `{endpoint, provider, label, projectId, receivedAt, contentType, body}` |

  Deleting a chat fires both `sessionDeleted` and `sessionClosed`, with no order between them.

Action args: `chatActions` get `{sessionId, messageId}`, `cardActions` get `{cardId}`.

**Error text**: what you throw is what people read. `throw new Error("x")` shows as `Error: x`, and `throw "x"` shows as `x`.

## The SDK

The global `lucode`. Something of the other target stays listed and throws a clear error. A call without its
permission throws too. Values cross as plain JSON: functions and class instances do not survive. A single value through
a call is at most 16 MB; what a call receives or returns, 32 MB.

### Both targets

| Call | Permission | Notes |
|---|---|---|
| `lucode.plugin` | | `{id, name, version}` |
| `lucode.context` | | `{projectId, sessionId, cardId, kind, target, deadlineMs}` |
| `lucode.context.cancelled()` | | True once cancelled or out of time. Check it in a long loop. |
| `lucode.context.progress(text)` | | Live feedback, shown by the chat timer. 500 characters, at most 5 a second: the rest are dropped. |
| `lucode.log.info/warn/error(...)` | | Ring of 500 lines of 4 KB per plugin, in Settings. Secrets are scrubbed. |
| `lucode.data.text(path)` `.json(path)` `.list(dir)` | | Read-only, `data/` in the zip. Paths are relative to it. |
| `lucode.memory.get/set/delete(key)` | | JSON, per plugin, 16 MB, process lifetime, per machine. |
| `lucode.time.now()` | | Milliseconds since the epoch. |
| `lucode.time.sleep(ms)` | | Cancellable. |
| `lucode.locks.with(name, fn)` | | Per-plugin mutex. Taking one this call already holds throws. |
| `lucode.http.fetch(url, {method, headers, body, timeoutMs})` | `http` | `{status, headers, text, json}` |

`lucode.http.fetch`: https only, allowlisted hosts only. Addresses are checked when connecting, so a name that resolves
to loopback, private, link-local or reserved space is refused, and every redirect is checked again. No cookies, no proxy,
no ambient credentials. Body is a string, up to 8 MB. Response up to 5 MB, or it throws. Timeout 30 s by default, 5 minutes
at most. `Host`, `Cookie`, `Content-Length` and similar headers are dropped. Header names come back in lowercase, and
`json` is null if the body is not JSON. A 404 does not throw: check `status`.

### Server only

| Call | Permission | Notes |
|---|---|---|
| `lucode.settings.get(key)` `.all()` | | The manifest's default if unset. Secrets are readable here, never by a browser or a runner. |
| `lucode.storage.get/set/delete(key)` | `storage` | JSON; `get` answers null if absent. 10 MB per plugin. |
| `lucode.storage.list(prefix)` | `storage` | Keys, up to 10000. |
| `lucode.conversations.get(sessionId)` | `conversations` | The row, or null. |
| `lucode.conversations.list()` | `conversations` | This project's rows, newest first. |
| `lucode.conversations.save({sessionId, url, title, source, turns, meta})` | `conversations` | Answers the row. The url must be http or https. |
| `lucode.conversations.bind(id, sessionId)` | `conversations` | |
| `lucode.sessions.get(id)` | `sessions` | `{id, name, model, kind, status, cardId, messageCount}`. Throws for another project. |
| `lucode.sessions.messages(id, {limit})` | `sessions` | Newest `limit` (100, at most 1000): `{role, content, seq, createdAt}`. |
| `lucode.board.listLists()` | `board: read` | `{id, name, position, kind, ...}`. |
| `lucode.board.listCards({listId})` | `board: read` | Not archived. Omit `listId` for all. |
| `lucode.board.getCard(id)` | `board: read` | Throws if it is not in this project. |
| `lucode.board.createCard({title, description, status, agent})` | `board: write` | `{id}` |
| `lucode.board.moveCard(id, {toKind \| toListId})` | `board: write` | `{moved, rejected}`. A board rule can refuse it. |
| `lucode.board.comment(id, text)` | `board: write` | |
| `lucode.board.addTag(id, tag)` | `board: write` | |
| `lucode.flows.emit(name, payload)` | `flows` | Fires `trigger.plugin_event`. 64 KB, 50 per call. |
| `lucode.browser.*` | `browser` | `launch({headless})`, `close()`, `status()`, `tabs()`, `newTab(url)`, `switchTab(id)`, `closeTab(id)`, `info()`, `navigate(url)`, `find({role,name,exact,limit,selector})`, `click(ref)`, `type(ref,text,{submit,clear})`, `press(combo)`, `evalPage(js)`, `text(ref,max)` |
| `lucode.runners.list()` | | Runners the hub sees: `{id, hostname, policy, version}`. |
| `lucode.runners.call(runnerId, rpcName, args, {workspace, timeoutMs})` | | Your runner code's rpc. `workspace` is `{sessionId}` or `{cardId}`, never a folder. |
| `lucode.relay.status()` | `relay` | `{configured, connected, url}`. Never the hub's key. |
| `lucode.relay.createEndpoint({provider, label})` | `relay` for that provider | `{id, url, secret, provider, label, projectId, created}`. The secret is only in this answer. At most 10 per plugin. Throws if the relay is not connected. |
| `lucode.relay.listEndpoints()` | `relay` | This plugin's endpoints, without secrets. |
| `lucode.relay.deleteEndpoint(id)` | `relay` | Gives an address back. Another plugin's is "no such webhook endpoint". |

The board is always the invocation's project, and every write goes through the same rules as anyone else's and is
recorded with your plugin as its cause. `lucode.browser` is one Edge per plugin with its own profile and a port the host
chooses. It is closed when the plugin is turned off. `navigate` and `newTab` go only to your allowed origins or exactly
`about:blank`. `find`, `click`, `type`, `press`, `text` and `evalPage` check the page's origin right before they run, because a
page can move by itself: on any other page, `about:blank` included, they throw. `info`, `tabs`, `switchTab` and `closeTab` work
anywhere. List every origin your plugin operates on, including ones Google or a sign-in sends you through.

### Runner only

| Call | Permission | Notes |
|---|---|---|
| `lucode.fs.read(path)` | `fs: read` | Text, up to 5 MB. |
| `lucode.fs.write(path, text)` | `fs: write` | Up to 20 MB. |
| `lucode.fs.list(path)` | `fs: read` | `{name, dir}`, up to 5000, secret names left out. |
| `lucode.fs.exists(path)` | `fs: read` | |
| `lucode.fs.stat(path)` | `fs: read` | `{size, dir, modified}`, or null. |
| `lucode.git.status()` | `git` | The text of `git status --porcelain=v1 --branch`. |
| `lucode.git.diff({staged, base, path})` | `git` | Secret files are left out. |
| `lucode.git.log({limit})` | `git` | `{sha, author, date, subject}`. 20, at most 500. |
| `lucode.git.branch()` | `git` | |
| `lucode.exec.run(cmd, argv, {timeoutMs, cwd})` | `exec` | `{code, stdout, stderr, truncated}`. 60 s by default. |
| `lucode.server.call(rpcName, args)` | | Your own plugin's server rpc, through the hub. Args up to 900 KB. |

Paths are relative to the workspace, which the hub sets from the session or card. Links are followed before the check, so
nothing escapes through one. With no workspace, `fs`, `git` and `exec` throw. Refused anywhere in a path: `.git`, `.env*`,
`*.pem`, `*.key`, `*.p12`, `*.pfx`, `id_rsa*`, `id_dsa*`, `id_ecdsa*`, `id_ed25519*`, `.lucode*`, `.ssh`, `.aws`, `.gnupg`,
`.npmrc`, `.netrc`, `.pgpass`, `.git-credentials`, `.docker`, `.kube`, `credentials*`. Windows 8.3 aliases are refused too.

`lucode.exec.run` takes a base name from your list, with no shell and arguments as a list. It gets a scrubbed environment,
the workspace as cwd, and 400 KB per stream, and the whole process tree is killed on the deadline. It refuses a program
that resolved to somewhere the repository could have planted it and, on Windows, anything that would run through cmd.exe.
The runner refuses it altogether unless started with `--plugin-policy=full`.

**Settings never reach a runner by themselves.** If runner code needs a value, your server code passes it as an argument,
on purpose.

## Webhooks

lucode runs on a laptop, and services such as Telegram can only post to a public address. The hub keeps a connection
**it opened itself** to a webhook relay (a small server with a public name, see `docs/plugins.md`), and the relay hands
each webhook down it. A plugin never listens on anything.

1. The plugin asks for an address in the project it is called in, usually from a page action:
   `const ep = lucode.relay.createEndpoint({provider: "telegram", label: "my bot"})`.
2. It gives the service `ep.url` and `ep.secret`. For Telegram that is `setWebhook` with `url` and `secret_token`, done
   with `lucode.http.fetch` (so `api.telegram.org` is also in its `http` permission). The relay checks the secret on every
   request, so the plugin never receives a webhook from anyone else.
3. What the service posts comes back as the plugin's `events.webhook`, in that project:

```js
module.exports = {
  events: {
    webhook(e) {
      const update = JSON.parse(e.body);                      // exactly what the service sent
      lucode.storage.set("last", update);
      lucode.flows.emit("message", { text: update.message.text });   // a flow with Plugin event can react
    },
  },
};
```

**This is how a webhook starts a flow**: `lucode.flows.emit` fires `trigger.plugin_event` (plugin and event name), as
for any event. It needs the `flows` permission too.

Things to know:

- **What arrives is data**, not instructions. Anyone who can reach the service can write the text, so treat it like any
  other outside input, and decide who a bot talks to in your own code (the Telegram plugin only reacts to the chats its
  settings allow).
- **An event may arrive twice.** The hub acknowledges it to the relay once the handler returns, and one whose
  acknowledgement was lost comes again on the next connection. Tell them apart by what the service put in them (Telegram's
  `update_id`). A handler that throws only gets a log line: the event is acknowledged anyway, since it would only throw again.
- **Events wait while the hub is away** (the relay keeps up to 1000 for 24 hours), and arrive in order when it
  reconnects. A plugin that is off, or has no `webhook` handler, drops them.
- **Only the owner of an address gets its events**, and only while it holds the `relay` permission for that provider.
  Uninstalling a plugin gives its addresses back to the relay.
- **No relay, no webhooks.** `lucode.relay.status()` says `configured: false` when the hub has none, and `createEndpoint`
  throws. A relay is something an operator sets up once for the hub, not something a plugin can bring.
- The handler has the usual 10 seconds, and the next webhook waits for it: keep it short.

## View trees

A page or an action returns JSON, and the host draws it. Unknown types and fields are dropped, and nothing in it is ever
inserted as markup except `markdown`, which goes through a renderer that shows raw HTML as text. A tree is cut off past
depth 24 or 4000 nodes.

| Type | Fields |
|---|---|
| `stack` | `direction` (`column`, `row`), `gap` (0 to 48), `children` |
| `grid` | `columns` (1 to 6; one on a phone), `children` |
| `card` | `title`, `subtitle`, `icon`, `children` |
| `heading` | `text`, `level` (1 to 4) |
| `text` | `text`, `tone` |
| `markdown` | `text`. Links http and https only; an image shows as a link and is not fetched. |
| `stat` | `label`, `value`, `delta`, `tone` |
| `badge` | `text`, `tone` |
| `table` | `columns` (strings or `{key, label, align}`), `rows` (arrays or objects), up to 1000 |
| `progress` | `value`, `max` (100), `label`, `tone` |
| `list` | `items`: strings, nodes, or `{title, subtitle, badge, href, action}` |
| `chart` | `kind` (`bar`, `line`), `title`, `labels`, `series`: `{name, data}`; data is numbers, or `{x\|label, y\|value}`. Five series at most, in the host's colors. |
| `button` | `label`, `icon`, `variant` (`primary`, `secondary`, `ghost`, `danger`), `action` |
| `form` | `fields`, `submit` (an action), `submitLabel` |
| `link` | `href` (http or https, otherwise only the words show), `text` |
| `divider` | |
| `empty` | `title`, `text`, `icon` |
| `conversation` | `conversationId`, `model` |

`tone`: `default`, `muted`, `success`, `warning`, `danger`, `accent`. `icon`: a path to an image in your zip. Colors and
styles of your own are ignored on purpose, so every plugin looks like lucode.

**Action**: `{rpc, args, confirm}`. `confirm` is `true` or your own question. The rpc may return a new tree, which replaces
the page, or `{toast: "text"}` or `{toast: {message, tone}}`. Anything else leaves the page as it is. A throw shows its
message beside what was clicked.

**Form**: `fields` are `{key, label, type, default, options, help, placeholder, required}`, with `type` as in settings. On
submit the rpc receives the action's args, each value by its key, and all of them under `values`.

**Conversation** embeds the app's own chat, with its history beside it, so a page holds its own chats. `conversationId`
names a `conversations` contribution or a saved conversation, and `model` is one of your models. Its sessions are of kind
`plugin` and stay out of the session list.

## Flow nodes and blueprints

A node's type in a graph is `plugin.<pluginId>.<nodeId>`, and its category is `action`. The palette and form come from the
manifest. Your code gets `config` already worked out: a `template` field rendered, such as `{{card.title}}`, an `expr` field
evaluated, and an unset field with its default. Nodes never run in a tool-gate path. A node with target `runner` runs on the
card's runner, so it needs a run with a card: a Schedule run has none, and neither does any trigger that names no card.

If the hub stops while your node runs, the node takes `failed` when it comes back, and nothing runs again, because your code
may already have acted. Each node counts against the run's limit of agent and AI turns.

A blueprint is a graph file:

```json
{
  "nodes": [
    { "id": "n1", "type": "trigger.schedule", "x": 40, "y": 160, "config": { "minutes": 60 } },
    { "id": "n2", "type": "plugin.acme.hello.ask", "x": 320, "y": 160, "config": { "question": "How is it going?" } }
  ],
  "edges": [{ "from": "n1", "port": "next", "to": "n2" }]
}
```

It appears in each project as a locked flow, turned off, validated like any other graph; one that does not validate is not
added and the plugin's row shows why. An upgrade publishes a new version. Two things to check when you write one:

- **`action.notify_supervisor`, like `move_card` and the other card nodes, needs a card.** A `trigger.schedule` run has none,
  and such a node fails the run. Use `action.push_notification` there.
- A `failed` output you do not connect makes the run fail visibly, which is usually what you want.

`trigger.plugin_event` starts a flow from `lucode.flows.emit`. Its fields are `plugin` and `event`.

## Limits of an invocation

| Limit | Value | When passed |
|---|---|---|
| rpc, page render | 15 s | "the plugin ran out of time" |
| Event handler | 10 s | |
| Flow node | `timeoutSeconds`, 300 s by default, 900 at most | |
| Model turn | `timeoutSeconds`, 600 s by default, 900 at most | |
| Code | 5 MB per .js file | |
| Call stack | 2000 calls | "the plugin went too deep" |
| Concurrency | 8 per plugin, the rest queue | |
| Regular expression | 5 s per match | |
| Built-in array operations, `Array.from`, `split` results | 1,048,576 elements | RangeError |
| Built-in string operations, and what `repeat`, `pad*`, `concat`, `join`, `replace` would make | 33,554,432 characters | RangeError |
| Regular expression input, source | 4,194,304 characters, 65,536 characters | RangeError |
| Object methods on a string | 1,048,576 characters | RangeError |
| Arguments of `apply`, `Reflect.apply`, `Reflect.construct`, `String.raw` | 65,536 | RangeError |
| A length that is a getter or an object, given to a built-in | refused | TypeError |
| JSON nesting | 512 levels | SyntaxError |

Not there at all: `eval`, `Function` and the constructors of function kinds, `Proxy`, `WeakRef`, `FinalizationRegistry`,
`ArrayBuffer`, `SharedArrayBuffer`, `DataView`, `Atomics`, typed arrays, `BigInt` (a file with a BigInt literal does not
load), timers, and `require` outside the zip. Array and string iterators are written in script, so a long spread or `for...of`
can be interrupted, and is slower than you may expect.

A promise is only read as settled or not when your handler returns. With no timers nothing settles later, so a promise still
pending is an error. Just write synchronous code.

## Security notes

- A secret from settings goes through your code only. Do not return it from an rpc, put it in a node's output, or log it.
  The host scrubs known secret values from logs, errors and results as a second line, not as a license.
- Treat anything from a card, a message or a web page as data. It may have been written to trick your plugin.
- What goes to and from a runner is encrypted and signed once an operator has paired it (`docs/plugins.md`). Nothing of yours
  needs to do that. A runner node on an unpaired runner takes `failed`.
- `browser` is high risk: code you run in a page can send what it reads anywhere, whatever your origins say.

## Known limitations

- **The heap watchdog is coarse.** There is one heap per process. lucode samples it during invocations and stops any invocation
  that was running when it grew about 256 MB past where that invocation began, after a garbage collection. Two plugins running
  at once share the blame. It keeps a runaway plugin from taking the hub down, and it is not a quota you can program against.
- **Not every allocation can be checked.** Object spread or rest of a long string (`{...text}`) and `+` on very long strings are
  single steps of the script engine that no size check can reach. They are stopped by the watchdog after the step, so they can
  go far past the cap first.
- **A native call that ignores its deadline is abandoned, not killed.** After 3 seconds the caller gets an error, and that
  call keeps its concurrency slot until it returns. Only your plugin waits for it.
- **No shared state in variables**, by design, and `lucode.memory`, locks and logs are per machine: a server and a runner of the
  same plugin do not share them.
- `lucode.storage` has no transactions. Use `lucode.locks.with` around a read and write that must go together.
- The runner reports only 50 of a call's log lines back in its answer.
- Plugins have only been exercised against the fixtures and one kind of runner; see `CLAUDE.md`, "Hasta donde llegan las pruebas".

## A worked example

A page with a form that keeps notes per project, and a flow node that reads the count. `plugin.json`:

```json
{
  "id": "acme.notes",
  "name": "Notes",
  "version": "1.0.0",
  "sdk": 1,
  "icon": "assets/icon.svg",
  "targets": { "server": { "main": "server.js", "permissions": { "storage": true, "flows": true } } },
  "contributes": {
    "pages": [{ "id": "main", "label": "Notes", "icon": "assets/icon.svg", "render": "renderMain" }],
    "flowNodes": [{
      "id": "count", "label": "Count notes", "ports": ["next"],
      "description": "Reads as nodes.<id>.count."
    }]
  }
}
```

`server.js`:

```js
"use strict";

const keyOf = projectId => "notes/" + projectId;
const read = projectId => lucode.storage.get(keyOf(projectId)) || [];

function renderMain(args) {
  const notes = read(args.projectId);
  return {
    type: "stack",
    gap: 16,
    children: [
      { type: "stat", label: "Notes", value: notes.length },
      {
        type: "card",
        title: "Add a note",
        children: [{
          type: "form",
          fields: [{ key: "text", label: "Note", type: "textarea", required: true }],
          submit: { rpc: "add" },
          submitLabel: "Add",
        }],
      },
      notes.length
        ? { type: "list", items: notes.map(n => ({ title: n.text })) }
        : { type: "empty", title: "No notes yet" },
    ],
  };
}

// Two people can add at once: the read and the write go together.
function add(args) {
  const text = String(args.text || "").trim();
  if (!text) throw new Error("A note needs some text.");
  lucode.locks.with("notes", () => {
    lucode.storage.set(keyOf(args.projectId), read(args.projectId).concat([{ text }]));
  });
  return renderMain(args); // a new tree replaces the page
}

function count(input) {
  return { port: "next", output: { count: read(input.projectId).length } };
}

module.exports = { rpc: { renderMain, add }, nodes: { count } };
```

Zip it with `plugin.json` at the root and `assets/icon.svg` beside it, and drop it in Settings, Plugins. The review dialog
lists "Own storage" and "Flows". `add` is reachable from the browser because `renderMain` offered it as an action.
