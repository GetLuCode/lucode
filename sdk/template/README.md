# Plugin template

The smallest plugin with both targets: a page on the server (`server.js`) and a flow node on the
runner (`runner.js`). Everything else is in `docs/plugin-sdk.md`.

## Start

1. Copy this folder and `../lucode-plugin.d.ts` next to each other, as they are here.
2. In `plugin.json`, change `id` (`yourname.something`; `lucode.*` is reserved), `name` and `author`.
3. Delete what you do not need. A plugin with no runner code drops `targets.runner` and the flow
   node. Every permission you remove is one less line in the review dialog.

To have your editor check the manifest, point it at `plugin.schema.json` in its own settings. Do not
add a `$schema` field to the file you package: the hub refuses fields it does not know.

## Package

`plugin.json` has to be at the root of the zip, and the zip takes only these extensions: `.json .js
.md .txt .html .css .svg .png .webp .jpg .jpeg`.

```
go build -o lucode-plugin ./cmd/lucode-plugin    # from the lucode repository
lucode-plugin validate my-plugin
lucode-plugin pack my-plugin -o my-plugin.zip
```

It runs the same checks as the hub, and leaves out `*.d.ts` and names starting with `.` or `_`. `lucode-plugin new
yourname.something` starts a plugin with a TypeScript backend, a page in Preact and tests (`lucode-plugin test`);
`-plain` starts a smaller one than this, with nothing to build. Pages that draw themselves: "Frontend pages" in
`docs/plugin-sdk.md`.

## Install

Settings, Plugins, drop the zip. The review dialog lists the permissions; tick them to install.

To upgrade, raise `version` and drop the new zip. A new permission needs a new grant.

## Check it

- The page appears as a tab of the project. Change the Greeting in Settings, Plugins, Hello, and
  reload the page.
- In Automate, the node shows up in the palette under the plugin's name, tagged `runner`. It needs
  a run with a card, and a runner with the plugin's code delivered (Settings, Plugins, Hello, Runners).
- `lucode.log` writes to Settings, Plugins, Hello, Logs. An error thrown by your code ends up there too.
