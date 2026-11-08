// Types of a plugin's frontend page: the bridge to its own backend, as the module "@lucode/ui".
//
// A page runs in a Worker of the browser, never in the app's page. It has Preact, its own small
// document (document.body is the page's area) and nothing else: no network, no storage of the app,
// no other tabs. What it draws is checked by the app before it is shown (docs/plugin-sdk.md,
// "Frontend pages"). Its only way to act is an rpc of its own plugin that plugin.json lists in the
// page's "rpc".

declare module "@lucode/ui" {
  type Json = string | number | boolean | null | Json[] | { [key: string]: Json };

  /** Where the page runs. */
  export const context: {
    readonly pluginId: string;
    readonly pluginName: string;
    readonly pluginVersion: string;
    readonly pageId: string;
    /** The project the page is open in. Every rpc gets it as args.projectId too. */
    readonly projectId: string;
  };

  /**
   * Calls an rpc of this plugin's backend. Only the rpcs the page lists in plugin.json are
   * reachable; any other is refused before it leaves the browser, and again by the hub. The
   * arguments go as JSON. A throw in the rpc rejects with its message.
   */
  export function rpc<T = any>(name: string, args?: { [key: string]: Json | undefined }): Promise<T>;

  export type Tone = "default" | "muted" | "success" | "warning" | "danger" | "accent";

  /** A short message in the app's toasts, under the plugin's name. */
  export function toast(message: string, tone?: Tone): void;

  /** Opens an http or https address in a new tab. A plain <a href> does the same when clicked. */
  export function openLink(url: string): void;

  /** An image from the zip, for <img src={asset("assets/chart.png")} />. Images are not fetched from anywhere else. */
  export function asset(path: string): string;

  /**
   * What the app tells the page: "webhook" (one reached this plugin), "theme" ("dark" or "light").
   * Returns a function that stops listening.
   */
  export function on(event: "webhook" | "theme" | (string & {}), handler: (data: any) => void): () => void;
}

declare module "*.css";
declare module "*.svg" { const url: string; export default url; }
declare module "*.png" { const url: string; export default url; }
declare module "*.jpg" { const url: string; export default url; }
declare module "*.webp" { const url: string; export default url; }
