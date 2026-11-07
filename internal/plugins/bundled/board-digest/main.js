// Board digest: a page and a flow node over the project's board. It only reads the board; what it
// keeps in its storage is one small snapshot per project per day, for the trend on the page.
"use strict";

const { summarize, sentence, dayKey, DAY_MS } = require("./lib/digest.js");

const DEFAULT_STALE_DAYS = 7;
const KEEP_SNAPSHOT_DAYS = 60;
const STALE_SHOWN = 12;

function staleDays(value) {
  const n = Number(value);
  if (n >= 1) return Math.floor(n);
  const saved = Number(lucode.settings.get("stale_days"));
  return saved >= 1 ? Math.floor(saved) : DEFAULT_STALE_DAYS;
}

function readBoard(days) {
  return summarize(lucode.board.listLists(), lucode.board.listCards(), lucode.time.now(), days);
}

// ---- snapshots ------------------------------------------------------------------------------

function snapshotPrefix(projectId) {
  return "snapshot/" + projectId + "/";
}

function keepSnapshot(projectId, digest) {
  const prefix = snapshotPrefix(projectId);
  const today = dayKey(lucode.time.now());
  lucode.storage.set(prefix + today, { day: today, open: digest.open, finished_7d: digest.finished_7d });
  const oldest = dayKey(lucode.time.now() - KEEP_SNAPSHOT_DAYS * DAY_MS);
  for (const key of lucode.storage.list(prefix)) {
    if (key.slice(prefix.length) < oldest) lucode.storage.delete(key);
  }
}

function snapshots(projectId) {
  const out = [];
  for (const key of lucode.storage.list(snapshotPrefix(projectId))) {
    const s = lucode.storage.get(key);
    if (s && typeof s.open === "number") out.push(s);
  }
  return out.sort((a, b) => (a.day < b.day ? -1 : 1));
}

// ---- the page -------------------------------------------------------------------------------

const shortDay = day => day.slice(5);

function renderStats(d) {
  return {
    type: "grid",
    columns: 4,
    children: [
      { type: "stat", label: "Open", value: d.open, delta: d.total + " on the board", tone: "muted" },
      { type: "stat", label: "In progress", value: d.in_progress, delta: d.review > 0 ? d.review + " in review" : "", tone: "muted" },
      { type: "stat", label: "Finished, 7 days", value: d.finished_7d, tone: "success" },
      {
        type: "stat",
        label: "Stale",
        value: d.stale,
        delta: "No change for " + d.stale_days + " days",
        tone: d.stale > 0 ? "warning" : "muted",
      },
    ],
  };
}

function renderCharts(d, history) {
  const cards = [
    {
      type: "card",
      title: "Cards per column",
      children: [{
        type: "chart",
        kind: "bar",
        labels: d.columns.map(c => c.name),
        series: [{ name: "Cards", data: d.columns.map(c => c.count) }],
      }],
    },
    {
      type: "card",
      title: "Finished per day",
      subtitle: "The last 14 days",
      children: [{
        type: "chart",
        kind: "bar",
        labels: d.finished_per_day.map(f => shortDay(f.day)),
        series: [{ name: "Finished", data: d.finished_per_day.map(f => f.count) }],
      }],
    },
  ];
  // A line needs two points: the trend appears from the second day on.
  if (history.length >= 2) {
    cards.push({
      type: "card",
      title: "Open tasks over time",
      subtitle: "One reading a day, on days the digest ran or this page was opened",
      children: [{
        type: "chart",
        kind: "line",
        labels: history.map(s => shortDay(s.day)),
        series: [{ name: "Open", data: history.map(s => s.open) }],
      }],
    });
  }
  return { type: "grid", columns: 2, children: cards };
}

function renderStale(d) {
  const shown = d.stale_tasks.slice(0, STALE_SHOWN);
  const more = d.stale - shown.length;
  return {
    type: "card",
    title: "Stale tasks",
    subtitle: "Open, outside the backlog, with no change for " + d.stale_days + " days. The oldest first.",
    children: shown.length === 0
      ? [{ type: "empty", title: "Nothing has gone quiet", text: "Every open task has changed recently." }]
      : [
          {
            type: "list",
            items: shown.map(t => ({ title: t.title, subtitle: t.column, badge: t.days + " days" })),
          },
        ].concat(more > 0 ? [{ type: "text", tone: "muted", text: more + " more not shown." }] : []),
  };
}

function renderMain(args) {
  const projectId = args.projectId || lucode.context.projectId;
  const d = readBoard(staleDays());
  if (d.total === 0) {
    return { type: "empty", title: "The board is empty", text: "This page fills up as tasks appear on the board." };
  }
  keepSnapshot(projectId, d);
  return {
    type: "stack",
    gap: 16,
    children: [
      { type: "text", tone: "muted", text: sentence(d) },
      renderStats(d),
      renderCharts(d, snapshots(projectId)),
      renderStale(d),
    ],
  };
}

// ---- the flow node --------------------------------------------------------------------------

function digest(input) {
  const d = readBoard(staleDays(input.config && input.config.stale_days));
  keepSnapshot(input.projectId || lucode.context.projectId, d);
  return { port: "next", output: Object.assign({ summary: sentence(d) }, d) };
}

module.exports = {
  rpc: { renderMain },
  nodes: { digest },
};
