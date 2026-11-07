# Plugins

A plugin adds things to lucode: a model for the chat, a page for a project, nodes and flows for
Automate, buttons on messages and cards. It comes as a zip and runs in a sandbox. This document is
for whoever installs and runs them. To write one, see `docs/plugin-sdk.md`.

## What a plugin can and cannot do

Plugin code is JavaScript that runs in a sandbox lucode embeds. It has no access to the disk, to
processes or to the network on its own. It only has the calls lucode gives it, and most of those need
a permission you grant when you install it.

No code of a plugin ever runs in your browser, because the page holds your login. What a plugin sends
does reach the page, always through lucode:

- A plugin's page is data that lucode draws with its own components, with no styles of its own.
- Its icons are images, shown in an `<img>`, where an svg runs nothing.
- Answers from its model are shown like any chat answer: markdown turned into HTML and cleaned. Scripts,
  forms and styles are removed, and it keeps none of the app's classes. In an answer from a plugin's model,
  a remote image shows as a link you can click and is not loaded, and a diagram that could load something
  shows as its source.
- Whatever a plugin shows you is framed by lucode with the plugin's name, so it cannot pass for the app.

## Installing

Installing, upgrading, turning a plugin on or off, changing its settings and reading its logs all need
an admin. Everyone else sees the same list, read-only.

There are two ways in, both in Settings, Plugins:

- **Drop a zip**, or choose a file. lucode checks the zip and opens the review dialog. Nothing is
  installed until you confirm it.
- **Included with lucode**: plugins in the binary, installed with one click.

A plugin that was removed stays removed. The ones included with lucode go back to the list so you can
install them again.

A plugin included with lucode follows the binary: at each start, if its copy in the binary changed, it is upgraded by itself,
with the permissions that copy asks for (the added ones go to its log). It stays on or off as it was, its settings, data and flows
stay, and a blueprint is never turned on. If the new copy is broken the old one keeps working and shows the error. An
older binary leaves a newer version in place.

## The review dialog

It lists everything the plugin asks for, grouped by where it applies, each with what it allows and how
risky it is. You tick each one and install, or cancel. There is no installing with part of the list: a
plugin gets all of what it asks for, or it does not get in.

| Risk | Meaning |
|---|---|
| Low | Its own data, or reading the board. |
| Medium | Reaches something outside itself: the network, chats, changing the board, flows, reading workspace files. |
| High | Writes files or runs programs on a machine that has your code, or drives a browser. |

Things worth reading before you tick:

- **Network** permissions name each host. A plugin can reach those hosts and nothing else, and only over
  https. Each host is its own line, so a plugin cannot add one later without asking.
- **Browser** permissions name each origin. The plugin drives its own Edge and can only read or act on pages of
  those origins. Code it runs in such a page can still send what it reads anywhere, so this limits what the plugin
  reads, not where a page sends data. Treat it as handing the plugin your session on those sites.
- **Webhooks from X through the relay** means the plugin can get public addresses on your webhook relay for that
  service, and receives what the service posts to them, such as the messages people send a bot. Each service is its
  own line. It is data from outside, written by anyone who can reach the service: a plugin that acts on it should
  decide for itself whose messages count (see "Webhook relay").
- **Run the command X** means that program, on your runners, in the workspace. A plugin that can start
  `npm` or `go` can run whatever those can run, so treat it as running code on that machine.

**Upgrading**: dropping a newer zip of an installed plugin opens the same dialog and shows the versions.
What you granted before stays ticked. What is new is marked and starts unticked.

## Server and runner

A plugin's code runs on the server, on runners, or on both. Its row in Settings says which.

- **Server** means the hub. It can reach the project's board, chats, its own storage and its settings. It
  has no access to your code.
- **Runner** means the machines that hold your code and credentials, so the rules there are stricter. It
  only sees the workspace of the session or card it runs for, and the hub decides which workspace that is,
  never the plugin. It never receives the plugin's settings by itself.

Secret files are refused on a runner even with the permission: `.git`, `.env*`, keys and certificates
such as `*.pem`, `*.key` and `id_rsa*`, `.ssh`, `.aws`, `.npmrc`, `credentials` and the like. They are not
even listed.

### Pairing a runner

A runner refuses every plugin until it is paired, and no message from the network can pair it:

1. An admin creates a code for that runner in Settings, Plugins, a plugin, **Runners**
   (`POST /api/plugins/runners/{runnerId}/pair`). The code is shown once and holds a secret: treat it like a password.
2. The operator runs the command it comes with, on the runner's machine:

   ```
   lucode-runner pair <code>
   ```

   Add `--data <dir>` if the runner uses another data folder. It writes the pairing with owner-only permissions and exits.
   A running runner picks it up with no restart. A code only works for the runner it was made for.

Creating a new code replaces the secret at once, so it is also how a runner is revoked: until the new code is used, the
hub and that runner refuse each other.

Everything between a hub and a paired runner, in both directions, is encrypted and authenticated with a key from that
secret: packages, invocations and their arguments, answers, and calls back to the hub. What the hub sends is also signed with
its own key, so one compromised runner cannot speak as the hub to another. The network can neither read these messages nor
forge or replay them. An unpaired runner's flow nodes take their `failed` output, with a message saying to pair it.

### --plugin-policy

Each runner has the last word on what plugins may do on it, whatever was granted on the hub:

```
lucode-runner --plugin-policy=standard
```

| Policy | Effect |
|---|---|
| `off` | Refuses every plugin. |
| `standard` (the default) | Files, git and network if granted. Running programs is refused. |
| `full` | Also allows running the programs that were granted. |

A grant can never go past the policy. Changing it means restarting the runner, which also stops the
`claude` processes it runs.

Settings, Plugins, a plugin, **Runners** lists each runner: online or not, paired or not, its policy, whether it has the
plugin and which version. If a policy blocks something the plugin needs, it says so there, with the flag to
change.

A flow node that runs on a runner uses the card's runner. If that runner is offline, not paired, or its policy refuses,
the node takes its `failed` output with a message saying why.

## Webhook relay

lucode runs on your machine, and services such as Telegram can only post to a public address. A **webhook relay**
(the `lucode-webhook-relay` server, run somewhere with a name and TLS) takes those posts and passes them to your hub over
a websocket **your hub opens**: nothing ever connects to your machine. Plugins that need webhooks (the `relay` permission)
use it through `lucode.relay`; without one, they are told so.

Set it up once, as the operator of the relay and the hub:

1. On the relay's server, make a tenant for this hub: `sudo relay-admin POST /admin/tenants '{"name":"my hub"}'`. The answer has
   a key, `lrk_...`, shown only once. (This is the relay's admin, with its own token: not a lucode admin.)
2. On the hub, put it in `relay.json` in the lucode data folder (`~/.lucode/relay.json`):

   ```json
   { "url": "wss://relay.example.com/ws", "tenant": "id from step 1", "name": "my hub", "key": "lrk_..." }
   ```

   The key never goes to a browser or a plugin. `url` must be `wss://` (`ws://` only for this machine). Restart the hub: it
   reads the file at start.
3. The hub log says `[relay] connected as ...`. A wrong key shows as `the relay refused this hub`, and the hub tries again
   every minute. With no file the hub simply has no relay.

While the hub is off the relay keeps what arrives (up to 1000 events for 24 hours) and hands it over, in order, when the hub
is back. The relay lives in memory: restarting it forgets what was waiting, not the tenants or addresses.

An address belongs to the plugin that asked for it and to the project it was asked in; uninstalling the plugin gives it back.
Only that plugin gets its events. The relay checks the sender (Telegram's `secret_token`) before anything reaches the hub.

## Using what a plugin adds

- **Models** appear in the mode selector of the Supervisor panel and in a chat's model menu. If the plugin
  is turned off or removed the entry goes away. A chat set to it falls back to the Supervisor and says so.
- **Pages** appear as tabs of each project.
- **Flow nodes** appear in the Automate palette under the plugin's name. Forms work like any other node's.
- **Blueprints** appear in each project as a **locked** flow, turned off. You can read it, run it and turn it
  on, but not edit it, and an upgrade of the plugin updates it. **Create a new version** gives you an ordinary
  copy to change. Uninstalling removes the locked flow and keeps your copies, which then show what is missing.
- **Message and card actions** appear in those menus.

Turning a plugin off stops what it has running, closes its browser and unloads it from runners. Its nodes stay
in your flows, flagged, and a run fails on them with a clear message until it is back on.

## Limits

| Limit | Value |
|---|---|
| Zip | 50 MB, 200 MB unpacked, 2000 files, 20 MB per file |
| Page or action call | 15 s |
| Event handler | 10 s |
| Flow node | 5 minutes by default, 15 at most |
| Model turn | 10 minutes by default, 15 at most |
| Calls running at once, per plugin | 8; the rest wait |
| Storage | 10 MB per plugin |
| Network response | 5 MB; https only; no private addresses |
| Built-in array operations | 1 million elements; longer ones fail |
| Built-in string operations | 32 million characters; longer ones fail |

A plugin whose code throws, loops or runs out of time is stopped, and only that call fails: the turn ends with its
error, and other plugins go on. The error shows in Settings, Plugins, a plugin, **Logs**. The sandbox is not a process
of its own, so this holds for what its limits catch; see the limitations below for what they do not.

## Known limitations

- **The sandbox is not a full security boundary against a determined attacker.** It takes away what could run
  text as code, reach the disk or start processes, and every call is checked. It runs inside the hub's or the
  runner's own process, though: a bug in the plugin runtime, or memory that grows faster than the watchdog below
  samples it, could still bring that process down. Install what you have reason to trust, as with any software
  that holds your grants.
- **A determined plugin can still run the process out of memory.** Plugin code runs inside the hub's or the runner's own
  process, and one step of the script engine cannot be stopped from outside. The built-in operations that could allocate
  without limit check their size first, but two cannot be checked: copying a long string with object spread or rest
  (`{...text}`), which makes a property per character, and `+` on very long strings, which can go a few GB past the
  memory limit before it is stopped. Only a separate process for plugins would close this, so install only what you have reason to trust.
- **The memory limit is coarse.** lucode watches the heap of the whole process while a plugin runs and stops a
  call when it grows by about 256 MB. It cannot tell two plugins apart, so one running beside a greedy one can
  be stopped too. It is there so a runaway plugin does not take the hub down, not as an exact quota.
- **Code stuck inside a slow native call** may not stop at once. After 3 seconds lucode gives up waiting and
  reports the failure, and that call can keep its slot until it returns. Only that plugin waits for it.
- **A browser plugin's pages can navigate.** A page on an allowed origin can go anywhere, and lucode does not stop it: the
  sign-in in the visible window has to work. Once it has left, the plugin can no longer read or act on it.
- **Other requests over NATS still have no authentication.** Plugin routes, plugin models and plugin flows refuse them;
  the rest of lucode's API over NATS is as it was.
- **Past answers in a chat that later changed model.** Images stay unloaded in a chat whose current model is a
  plugin's. If you switch that chat to another model, the answers it already holds are shown as an ordinary chat's.
- **The network check is by host and address**, not by content. A plugin with a host in its list can send that
  host whatever it can read.
- **Logs and `lucode.memory` live as long as the process.** A restart clears them.
- **A grant is per plugin, not per project.** An installed plugin works in every project, and reaches only the
  project it is called in.
- Tool-gate flows cannot use plugin nodes: those answer while a tool waits, and a plugin's time limits do not fit.
