// @ts-check
/// <reference path="../lucode-plugin.d.ts" />
// Runs on the runner, inside the card's workspace. The hub decides the workspace, never the plugin.
"use strict";

const sdk = /** @type {Lucode.RunnerSDK} */ (/** @type {unknown} */ (lucode));

const MAX_CHARS = 20000;

/** @param {Lucode.NodeInput} input */
function read_file(input) {
  const path = String(input.config.path || "");
  // A throw takes the "failed" output with this message, or fails the run if it is not connected.
  if (!sdk.fs.exists(path)) throw new Error("There is no file " + path + " in this workspace.");
  const text = sdk.fs.read(path);
  sdk.log.info("read", path, text.length, "characters");
  // the output is the answer the next nodes receive: here, the text itself
  return { port: "text", output: text.slice(0, MAX_CHARS) };
}

module.exports = {
  nodes: { read_file },
};
