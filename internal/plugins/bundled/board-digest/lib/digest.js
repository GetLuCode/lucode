// What the page and the flow node both say: the board's numbers, worked out from its lists and
// cards. No SDK calls in here, so it reads on its own.
"use strict";

const DAY_MS = 24 * 60 * 60 * 1000;
const FINISHED_DAYS = 14;
const RECENT_DAYS = 7;

function timeOf(text) {
  const t = Date.parse(text || "");
  return isNaN(t) ? 0 : t;
}

function dayKey(ms) {
  return new Date(ms).toISOString().slice(0, 10);
}

function plural(n, word) {
  return n + " " + word + (n === 1 ? "" : "s");
}

// lists: the board's lists. cards: every card of them.
function summarize(lists, cards, now, staleDays) {
  const live = cards.filter(c => !c.archived);
  const byList = {};
  for (const c of live) (byList[c.list_id] = byList[c.list_id] || []).push(c);

  const ordered = lists.slice().sort((a, b) => a.position - b.position);
  const columns = ordered.map(l => ({ id: l.id, name: l.name, kind: l.kind, count: (byList[l.id] || []).length }));
  const countKind = kind => columns.filter(c => c.kind === kind).reduce((n, c) => n + c.count, 0);
  const done = countKind("done");

  // The backlog is meant to wait, and Done is finished: neither goes stale.
  const quiet = [];
  const staleMs = staleDays * DAY_MS;
  for (const l of ordered) {
    if (l.kind === "backlog" || l.kind === "done") continue;
    for (const c of byList[l.id] || []) {
      const touched = timeOf(c.updated_at);
      if (touched && now - touched > staleMs) {
        quiet.push({ id: c.id, title: c.title, column: l.name, days: Math.floor((now - touched) / DAY_MS) });
      }
    }
  }
  quiet.sort((a, b) => b.days - a.days);

  // Finished per day, oldest first, with the empty days kept so the gaps show.
  const finished = [];
  for (let i = FINISHED_DAYS - 1; i >= 0; i--) finished.push({ day: dayKey(now - i * DAY_MS), count: 0 });
  let finishedRecent = 0;
  for (const c of live) {
    const t = timeOf(c.finished_at);
    if (!t) continue;
    const slot = finished.find(f => f.day === dayKey(t));
    if (slot) slot.count++;
    if (now - t <= RECENT_DAYS * DAY_MS) finishedRecent++;
  }

  return {
    total: live.length,
    open: live.length - done,
    in_progress: countKind("inprogress"),
    review: countKind("review"),
    blocked: countKind("blocked"),
    finished_7d: finishedRecent,
    stale: quiet.length,
    stale_days: staleDays,
    stale_tasks: quiet,
    columns,
    finished_per_day: finished,
  };
}

// One short paragraph, for a notification or a comment.
function sentence(d) {
  const parts = [
    plural(d.open, "open task"),
    d.in_progress + " in progress",
    d.finished_7d + " finished this week",
  ];
  if (d.blocked > 0) parts.push(d.blocked + " blocked");
  let text = parts.join(", ") + ".";
  if (d.stale > 0) {
    const oldest = d.stale_tasks[0];
    text += " " + plural(d.stale, "stale task") + ": oldest is \"" + oldest.title + "\", " + plural(oldest.days, "day") + " without a change.";
  }
  return text;
}

module.exports = { summarize, sentence, dayKey, DAY_MS };
