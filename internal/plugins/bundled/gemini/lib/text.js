// The text of an answer and of a conversation's name. No SDK calls in here.
"use strict";

const CHIP_COUNT = /^\+\d+$/;
const DISCLAIMER = /(puede cometer errores|can make mistakes|may include mistakes|verifica las respuestas|double-check)/i;

// clean tidies the Markdown of an answer: it drops the "+3" counters of source pills and the "AI
// can make mistakes" notice if they got through, and collapses blank lines.
function clean(raw) {
  const lines = [];
  for (const l of String(raw).split("\r").join("").split("\n")) {
    if (CHIP_COUNT.test(l.trim())) continue;
    lines.push(l.replace(/[ \t]+$/, ""));
  }
  while (lines.length > 0 && (lines[lines.length - 1].trim() === "" || DISCLAIMER.test(lines[lines.length - 1]))) {
    lines.pop();
  }
  return lines.join("\n").replace(/\n{3,}/g, "\n\n").trim();
}

// title is how a conversation is named in the history: what the user asked, on one line.
function title(text) {
  const t = String(text).split(/\s+/).filter(Boolean).join(" ");
  const chars = Array.from(t);
  return chars.length > 100 ? chars.slice(0, 100).join("") + "…" : t;
}

module.exports = { clean, title };
