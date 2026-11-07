// Types of the lucode plugin SDK: the `lucode` global, the manifest and view trees.
//
// Keep this file next to your plugin but out of the zip (.ts is not an allowed extension there),
// and start each JS file with:
//
//   // @ts-check
//   /// <reference path="./lucode-plugin.d.ts" />
//   const sdk = /** @type {Lucode.ServerSDK} */ (lucode);   // or Lucode.RunnerSDK in runner code
//
// The global itself is typed with only what both targets share, so a call of the wrong target is
// a type error once you say which target a file is for. At run time such a call throws a clear error.

declare namespace Lucode {
  type Json = null | boolean | number | string | Json[] | { [key: string]: Json };
  type JsonObject = { [key: string]: Json };
  type Target = "server" | "runner";

  // ---- Both targets ------------------------------------------------------------------------

  interface PluginInfo {
    readonly id: string;
    readonly name: string;
    readonly version: string;
  }

  interface InvocationContext {
    readonly projectId: string;
    readonly sessionId: string;
    /** The card this invocation is about, when there is one. */
    readonly cardId?: string;
    readonly kind: string;
    readonly target: Target;
    /** Milliseconds left when the invocation started. */
    readonly deadlineMs: number;
    /** True once the invocation was cancelled or ran out of time. */
    cancelled(): boolean;
    /** Live feedback, shown by the chat timer. Cut at 500 characters. */
    /** At most 5 a second: the rest are dropped. */
    progress(text: string): void;
  }

  /** Per-plugin ring buffer of 500 lines of up to 4 KB, visible in Settings. Process lifetime. */
  interface Log {
    info(...values: unknown[]): void;
    warn(...values: unknown[]): void;
    error(...values: unknown[]): void;
  }

  /** Read-only files under data/ in the zip. Paths are relative to data/. */
  interface Data {
    text(path: string): string;
    json<T = Json>(path: string): T;
    /** The files under a folder of data/, or all of them. */
    list(dir?: string): string[];
  }

  /** JSON values kept for the life of the process, per plugin, on this machine. 16 MB in all. */
  interface Memory {
    get<T = Json>(key: string): T | null;
    set(key: string, value: unknown): void;
    delete(key: string): void;
  }

  interface Time {
    /** Milliseconds since the epoch. */
    now(): number;
    /** Blocks this invocation. Throws if it is cancelled meanwhile. */
    sleep(ms: number): void;
  }

  /** A per-plugin mutex across invocations on this machine. Not reentrant: taking a lock this invocation holds throws. */
  interface Locks {
    with<T>(name: string, fn: () => T): T;
  }

  interface FetchOptions {
    /** GET by default. */
    method?: string;
    /** Host, Cookie, Content-Length, Connection and similar are dropped. */
    headers?: { [name: string]: string };
    /** Up to 8 MB. */
    body?: string;
    /** 30 s by default, 5 minutes at most. */
    timeoutMs?: number;
  }

  interface FetchResponse {
    status: number;
    /** Lowercase names; repeated headers joined with ", ". */
    headers: { [name: string]: string };
    /** Up to 5 MB: a larger response throws. */
    text: string;
    /** The body parsed, or null if it is not JSON. */
    json: Json;
  }

  /** https to the hosts of the target's http permission, and nowhere else. Needs: http. */
  interface Http {
    fetch(url: string, options?: FetchOptions): FetchResponse;
  }

  /** What every invocation has, on either target. */
  interface CommonSDK {
    readonly plugin: PluginInfo;
    readonly context: InvocationContext;
    readonly log: Log;
    readonly data: Data;
    readonly memory: Memory;
    readonly time: Time;
    readonly locks: Locks;
    readonly http: Http;
  }

  // ---- Server target ------------------------------------------------------------------------

  /** Values of contributes.settings. Secrets are readable here and never by the browser or a runner. */
  interface Settings {
    get<T = Json>(key: string): T;
    all(): JsonObject;
  }

  /** Persistent, per plugin, on the hub. 10 MB per plugin. Needs: storage. */
  interface Storage {
    get<T = Json>(key: string): T | null;
    set(key: string, value: unknown): void;
    delete(key: string): void;
    list(prefix?: string): string[];
  }

  interface ConversationRecord {
    sessionId: string;
    /** http or https only. */
    url?: string;
    title?: string;
    /** Which of the plugin's conversation lists this belongs to. */
    source?: string;
    turns?: number;
    meta?: unknown;
  }

  /** The plugin's own conversation list. Needs: conversations. */
  interface Conversations {
    get(sessionId: string): JsonObject | null;
    list(): JsonObject[];
    /** The session must be of the invocation's project. Answers the saved row. */
    save(record: ConversationRecord): JsonObject;
    bind(conversationId: string, sessionId: string): void;
  }

  /** Chats of the invocation's project only. Needs: sessions: "read". */
  interface Sessions {
    /** Throws if the session is not of this project. */
    get(id: string): { id: string; name: string; model: string; kind: string; status: string; cardId: string; messageCount: number };
    /** The newest ones: 100 by default, 1000 at most. */
    messages(id: string, options?: { limit?: number }): { role: string; content: string; seq: number; createdAt: string }[];
  }

  interface BoardList {
    id: string;
    project_id: string;
    name: string;
    position: number;
    /** backlog, todo, inprogress, review, done, blocked, or "" for a list you created. */
    kind: string;
    created_at: string;
  }

  interface BoardCard {
    id: string;
    list_id: string;
    project_id: string;
    title: string;
    description: string;
    card_type: string;
    tags: string[];
    agent_id: string;
    depends_on: string[];
    start_date: string;
    end_date: string;
    started_at: string;
    finished_at: string;
    position: number;
    archived: boolean;
    parent_id: string;
    created_at: string;
    updated_at: string;
    last_error?: string;
  }

  /** The invocation's project only. Reads need board: "read"; writes need board: "write". */
  interface Board {
    listLists(): BoardList[];
    /** Not archived. Every list's, unless you name one. */
    listCards(filter?: { listId?: string }): BoardCard[];
    /** Throws if the card is not of this project. */
    getCard(id: string): BoardCard;
    createCard(card: { title: string; description?: string; status?: string; agent?: string }): { id: string };
    /** `moved` is false, with the reason in `rejected`, if a rule of the board refuses it. */
    moveCard(id: string, to: { toKind?: string; toListId?: string }): { moved: boolean; rejected: string };
    comment(id: string, text: string): void;
    addTag(id: string, tag: string): void;
  }

  /** Needs: flows. Fires the trigger.plugin_event trigger. Payload up to 64 KB; 50 events per invocation. */
  interface Flows {
    emit(name: string, payload?: unknown): void;
  }

  /**
   * One Edge per plugin, with a profile and a port the host chooses. Needs: browser (high risk). find, click, type, press,
   * text and evalPage throw unless the current page is on an allowed origin; about:blank is not one.
   */
  interface Browser {
    launch(options?: { headless?: boolean }): unknown;
    close(): unknown;
    status(): unknown;
    tabs(): unknown;
    newTab(url: string): unknown;
    switchTab(id: string): unknown;
    closeTab(id: string): unknown;
    info(): unknown;
    navigate(url: string): unknown;
    find(query: { role?: string; name?: string; exact?: boolean; limit?: number; selector?: string }): unknown;
    click(ref: string): unknown;
    type(ref: string, text: string, options?: { submit?: boolean; clear?: boolean }): unknown;
    press(combo: string): unknown;
    evalPage(js: string): unknown;
    text(ref?: string, max?: number): string;
  }

  /** The runners the hub can see right now. */
  interface RunnerInfo {
    id: string;
    hostname: string;
    policy: "off" | "standard" | "full" | string;
    version: string;
  }

  interface Runners {
    list(): RunnerInfo[];
    /**
     * Calls an rpc of this plugin's runner target. `workspace` names a session or a card, never a
     * folder: the hub works out the folder. By default it uses the invocation's own. The timeout
     * never goes past this invocation's deadline.
     */
    call<T = Json>(runnerId: string, rpcName: string, args?: unknown, options?: { workspace?: { sessionId?: string; cardId?: string }; timeoutMs?: number }): T;
  }

  interface ServerSDK extends CommonSDK {
    readonly context: InvocationContext & { readonly target: "server" };
    readonly settings: Settings;
    readonly storage: Storage;
    readonly conversations: Conversations;
    readonly sessions: Sessions;
    readonly board: Board;
    readonly flows: Flows;
    readonly browser: Browser;
    readonly runners: Runners;
  }

  // ---- Runner target ------------------------------------------------------------------------

  /**
   * The invocation's workspace only: paths are relative to it and checked after following links.
   * .git and secret files are refused (see docs/plugin-sdk.md for the list), and list leaves them
   * out. With no workspace every call throws. Needs: fs: "read", and "write" for write.
   */
  interface Fs {
    /** Up to 5 MB. */
    read(path: string): string;
    /** Up to 20 MB. Creates the folders it needs. */
    write(path: string, text: string): void;
    /** Up to 5000 entries. */
    list(path?: string): { name: string; dir: boolean }[];
    exists(path: string): boolean;
    /** null if there is nothing there. */
    stat(path: string): { size: number; dir: boolean; modified: string } | null;
  }

  /** Reads the workspace's repository. Throws with no workspace. Needs: git: "read". */
  interface Git {
    /** The text of git status --porcelain=v1 --branch. */
    status(): string;
    /** Secret files are left out of the diff. */
    diff(options?: { staged?: boolean; base?: string; path?: string }): string;
    /** The newest commits: 20 by default, 500 at most. */
    log(options?: { limit?: number }): { sha: string; author: string; date: string; subject: string }[];
    branch(): string;
  }

  interface ExecResult {
    code: number;
    stdout: string;
    stderr: string;
    /** Each stream keeps 400 KB; true if something was cut. */
    truncated: boolean;
  }

  /**
   * Starts an allowlisted program by base name: no shell, a scrubbed environment, the workspace as
   * cwd, capped output, and the whole process tree killed on the deadline (60 s by default). High
   * risk. Refused by a runner unless it was started with --plugin-policy=full. Needs: exec.commands.
   */
  interface Exec {
    run(cmd: string, argv?: string[], options?: { timeoutMs?: number; cwd?: string }): ExecResult;
  }

  /** Calls an rpc of this plugin's own server target, through the hub. Args up to 900 KB. */
  interface ServerCall {
    call<T = Json>(rpcName: string, args?: unknown): T;
  }

  interface RunnerSDK extends CommonSDK {
    readonly context: InvocationContext & { readonly target: "runner" };
    readonly fs: Fs;
    readonly git: Git;
    readonly exec: Exec;
    readonly server: ServerCall;
  }

  // ---- What main.js exports -----------------------------------------------------------------

  interface TurnRequest {
    sessionId: string;
    projectId: string;
    message: string;
    image: { base64: string; mediaType: string } | null;
    model: string;
  }

  interface NodeInput {
    /** The node's fields as set in the editor. */
    config: { [field: string]: Json };
    card: JsonObject | null;
    event: JsonObject | null;
    /** Outputs of the earlier nodes of the run, by node id. */
    nodes: { [nodeId: string]: Json };
    run: JsonObject;
    projectId: string;
  }

  /** Throwing sends the run through the "failed" output, or fails it if that is not connected. */
  interface NodeResult { port: string; output?: { [key: string]: unknown } }

  /** Args also carry projectId and the ids the caller gave. */
  type RpcArgs = { projectId?: string; [key: string]: Json | undefined };
  type RpcResult = ViewNode | { toast: string | { message: string; tone?: Tone } } | unknown;

  interface EventHandlers {
    /** After this plugin is installed. Has no project. */
    installed?(e: { version: string }): void;
    /** After a new version of this plugin replaces the old one. Has no project. */
    upgraded?(e: { version: string }): void;
    /** Before this plugin is removed, if it is enabled. The removal waits for it, up to the deadline. */
    uninstalling?(e: JsonObject): void;
    /** A chat of the project was deleted. Goes to every enabled plugin, with a conversation in it or not. */
    sessionDeleted?(e: { sessionId: string; projectId: string }): void;
    /**
     * A chat this plugin has a conversation in is over: it was deleted, or a new Supervisor conversation
     * replaced it (that chat still exists and can come back). Only goes to plugins with a conversation in it.
     */
    sessionClosed?(e: { sessionId: string; projectId: string }): void;
    /** A card changed list. Goes to every enabled plugin with the board permission; stops past the flows' depth limit. */
    cardMoved?(e: { cardId: string; projectId: string; from_list: string; from_kind: string; to_list: string; to_kind: string }): void;
  }

  interface ServerExports {
    models?: { [id: string]: { turn(req: TurnRequest): string } };
    nodes?: { [id: string]: (input: NodeInput) => NodeResult };
    rpc?: { [name: string]: (args: RpcArgs) => RpcResult };
    events?: EventHandlers;
  }

  interface RunnerExports {
    nodes?: { [id: string]: (input: NodeInput) => NodeResult };
    rpc?: { [name: string]: (args: RpcArgs) => RpcResult };
  }

  // ---- View trees ---------------------------------------------------------------------------

  type Tone = "default" | "muted" | "success" | "warning" | "danger" | "accent";

  interface Action {
    /** An rpc of this plugin. */
    rpc: string;
    args?: JsonObject;
    /** Asks first: true for a plain question, or your own words. */
    confirm?: boolean | string;
  }

  interface FormField {
    key: string;
    label?: string;
    type?: "text" | "textarea" | "number" | "bool" | "select" | "secret";
    default?: string | number | boolean;
    options?: (string | { value: string; label?: string })[];
    help?: string;
    placeholder?: string;
    required?: boolean;
  }

  type TableColumn = string | { key: string; label?: string; align?: "left" | "right" | "center" };
  type ListItem = string | ViewNode | { title: string; subtitle?: string; badge?: string; href?: string; action?: Action };
  type ChartPoint = number | { x?: string; label?: string; y?: number; value?: number };

  /** A plugin draws nothing itself: the host draws these with its own components. Unknown types and fields are dropped. */
  type ViewNode =
    | { type: "stack"; direction?: "column" | "row"; gap?: number; children?: ViewNode[] }
    | { type: "grid"; columns?: number; children?: ViewNode[] }
    | { type: "card"; title?: string; subtitle?: string; icon?: string; children?: ViewNode[] }
    | { type: "heading"; text: string; level?: 1 | 2 | 3 | 4 }
    | { type: "text"; text: string; tone?: Tone }
    | { type: "markdown"; text: string }
    | { type: "stat"; label: string; value: string | number; delta?: string; tone?: Tone }
    | { type: "badge"; text: string; tone?: Tone }
    | { type: "table"; columns?: TableColumn[]; rows: (unknown[] | { [column: string]: unknown })[] }
    | { type: "progress"; value: number; max?: number; label?: string; tone?: Tone }
    | { type: "list"; items: ListItem[] }
    | { type: "chart"; kind?: "bar" | "line"; title?: string; labels?: string[]; series: { name: string; data: ChartPoint[] }[] }
    | { type: "button"; label: string; icon?: string; variant?: "primary" | "secondary" | "ghost" | "danger"; action: Action }
    | { type: "form"; fields: FormField[]; submit: Action; submitLabel?: string }
    | { type: "link"; href: string; text?: string }
    | { type: "divider" }
    | { type: "empty"; title?: string; text?: string; icon?: string }
    | { type: "conversation"; conversationId?: string; model?: string };

  // ---- plugin.json --------------------------------------------------------------------------

  interface ServerPermissions {
    storage?: boolean;
    conversations?: boolean;
    sessions?: "read";
    board?: "read" | "write";
    flows?: boolean;
    http?: { hosts: string[] };
    browser?: { origins: string[] };
  }

  interface RunnerPermissions {
    fs?: "read" | "write";
    git?: "read";
    exec?: { commands: string[] };
    http?: { hosts: string[] };
  }

  interface SettingDef {
    key: string;
    label: string;
    type: "text" | "textarea" | "number" | "bool" | "select" | "secret";
    default?: Json;
    options?: string[];
    help?: string;
  }

  interface NodeField {
    name: string;
    label?: string;
    kind: "text" | "template" | "expr" | "number" | "select" | "agent" | "list" | "bool" | "lines";
    required?: boolean;
    options?: string[];
    default?: Json;
    help?: string;
    min?: number;
    port_label?: string;
  }

  interface Manifest {
    id: string;
    name: string;
    version: string;
    sdk: 1;
    description?: string;
    author?: string;
    homepage?: string;
    icon?: string;
    targets?: {
      server?: { main: string; permissions?: ServerPermissions };
      runner?: { main: string; permissions?: RunnerPermissions };
    };
    /** Old form of targets.server.main. */
    main?: string;
    /** Old form of targets.server.permissions. */
    permissions?: ServerPermissions;
    contributes?: {
      models?: { id: string; label: string; mode?: string; hint?: string; icon?: string; accent?: string; images?: boolean; loader?: string; waitingText?: string; timeoutSeconds?: number }[];
      conversations?: { id: string; model: string; title: string; emptyText?: string; showUrl?: boolean }[];
      pages?: { id: string; label: string; icon?: string; render: string }[];
      settings?: SettingDef[];
      blueprints?: { key: string; name: string; description?: string; file: string }[];
      flowNodes?: { id: string; label: string; description?: string; icon?: string; fields?: NodeField[]; ports?: string[]; timeoutSeconds?: number; target?: Target }[];
      chatActions?: { id: string; label: string; icon?: string; rpc: string; target?: Target }[];
      cardActions?: { id: string; label: string; icon?: string; rpc: string; target?: Target }[];
    };
  }
}

/** The SDK. Cast it to Lucode.ServerSDK or Lucode.RunnerSDK in a file written for one target. */
declare const lucode: Lucode.CommonSDK;

/** CommonJS, inside the zip only. Paths start with ./ or ../ */
declare function require(path: string): any;
declare const module: { exports: unknown };
declare const exports: { [key: string]: unknown };
