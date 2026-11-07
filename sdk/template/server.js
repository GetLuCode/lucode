// @ts-check
/// <reference path="../lucode-plugin.d.ts" />
// Runs on the hub. A page is an rpc that returns a view tree: JSON the host draws. No code or HTML
// of a plugin ever reaches the browser.
"use strict";

const sdk = /** @type {Lucode.ServerSDK} */ (/** @type {unknown} */ (lucode));

/** @param {Lucode.RpcArgs} args */
function renderMain(args) {
  const name = typeof args.name === "string" && args.name.trim() ? args.name.trim() : "world";
  /** @type {Lucode.ViewNode} */
  const page = {
    type: "stack",
    gap: 16,
    children: [
      { type: "heading", text: sdk.settings.get("greeting") + ", " + name, level: 1 },
      { type: "text", tone: "muted", text: "Version " + sdk.plugin.version + " of " + sdk.plugin.name + "." },
      {
        type: "card",
        title: "Say hello to someone else",
        children: [
          {
            type: "form",
            fields: [{ key: "name", label: "Name", required: true }],
            submit: { rpc: "renderMain" },
            submitLabel: "Say hello",
          },
        ],
      },
    ],
  };
  return page;
}

module.exports = {
  rpc: { renderMain },
};
