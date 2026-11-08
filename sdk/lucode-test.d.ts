// Types of a test file of lucode-plugin test (test/**/*.test.ts). A test runs the plugin's backend
// in the same runtime as the hub's, with storage, settings and flows.emit in memory, and its pages
// in the same Worker runtime as the browser's, on a virtual clock: nothing is sent anywhere, and
// every action has finished when it returns, so a test needs no await.

declare function test(name: string, fn: () => void | Promise<void>): void;
declare namespace test {
  function only(name: string, fn: () => void | Promise<void>): void;
  function skip(name: string, fn?: () => void): void;
}

declare function assert(value: unknown, message?: string): asserts value;
declare namespace assert {
  function ok(value: unknown, message?: string): asserts value;
  function equal<T>(actual: T, expected: T, message?: string): void;
  function notEqual<T>(actual: T, unexpected: T, message?: string): void;
  /** The same JSON: objects key by key, arrays in order. */
  function deepEqual<T>(actual: T, expected: T, message?: string): void;
  /** A string that contains a string, or an array with an element deepEqual to it. */
  function includes(haystack: string | unknown[], needle: unknown, message?: string): void;
  function match(text: string, re: RegExp, message?: string): void;
  /** Returns what was thrown. `expected` is a part of the message, or a pattern of it. */
  function throws(fn: () => unknown, expected?: string | RegExp, message?: string): any;
  function fail(message?: string): never;
}

/** Selectors: tag, #id, .class, [attr], [attr=value], separated by spaces for descendants, or text=Words for the innermost element with exactly that text. */
type PageSelector = string;

interface PageUnderTest {
  /** The page's markup, or one element's. */
  html(selector?: PageSelector): string;
  /** The text, spaces collapsed. */
  text(selector?: PageSelector): string;
  count(selector: PageSelector): number;
  exists(selector: PageSelector): boolean;
  attr(selector: PageSelector, name: string): string | null;
  value(selector: PageSelector): string;
  checked(selector: PageSelector): boolean;
  disabled(selector: PageSelector): boolean;

  /** Clicks, and submits the form when it is a submit button. Throws on a disabled one. */
  click(selector: PageSelector): void;
  /** Types a value: input, then change. */
  input(selector: PageSelector, value: string): void;
  select(selector: PageSelector, value: string): void;
  check(selector: PageSelector, on?: boolean): void;
  submit(selector: PageSelector): void;
  /** keydown and keyup of one key, such as "Enter". */
  key(selector: PageSelector, key: string): void;
  dispatch(selector: PageSelector, type: string, detail?: { [key: string]: unknown }): void;
  /** What the app would tell the page, such as emit("webhook", {...}). */
  emit(event: string, data?: unknown): void;
  /** Lets ms of virtual time pass: timers and intervals fire. Each action already waits a second. */
  wait(ms: number): void;

  toasts(): { message: string; tone: string }[];
  /** Addresses the page opened. */
  opened(): string[];
  /** The rpcs the page called, refused ones too. */
  calls(): { name: string; args: unknown }[];
  errors(): string[];
}

declare const plugin: {
  readonly id: string;
  readonly name: string;
  readonly version: string;
  /** Calls an rpc as the browser does, with args.projectId ("test-project" unless opts says). Throws its error. */
  rpc<T = any>(name: string, args?: { [key: string]: unknown }, opts?: { projectId?: string }): T;
  /** Runs a flow node as the engine does: { config, card, event, nodes, run, projectId }. */
  node<T = any>(id: string, input?: { [key: string]: unknown }): T;
  /** The plugin's storage, empty at the start of each test. */
  readonly storage: { get(key: string): any; set(key: string, value: unknown): void; keys(): string[] };
  /** A setting's value for this test; the others keep their defaults. */
  setSetting(key: string, value: unknown): void;
  /** What lucode.flows.emit sent during this test. */
  events(): { event: string; payload: { [key: string]: unknown } }[];
  logs(): { level: string; message: string }[];
  /** Opens a frontend page. It is loaded, and its first rpcs answered, when this returns. */
  page(id: string, opts?: { projectId?: string }): PageUnderTest;
};
