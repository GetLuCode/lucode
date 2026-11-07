// Google AI Mode (Gemini) as a chat model: it answers by talking to the page in an Edge that the
// host drives. Each lucode session gets its own tab and so its own conversation: the page is
// opened, "Modo IA" / "AI Mode" is clicked, a first message tells Gemini how to answer, and from
// then on each message of the chat is typed into the page and the answer is read back.
"use strict";

const { clean, title } = require("./lib/text.js");

const ERROR_PREFIX = "Google AI Mode: ";
const CONVERSATION_SOURCE = "aimode";

// How long to wait for things on the page.
const PAGE_WAIT = 15 * 1000; // elements to show up
const RESUME_WAIT = 3 * 60 * 1000; // a long saved conversation to load
const GIVE_UP_AFTER = 20; // polls (10 s) without new turns before a saved conversation is given up
const TURN_WAIT = 25 * 1000; // the new turn to appear after sending
const ANSWER_WAIT = 3 * 60 * 1000; // the answer to finish
const POLL = 500;
const STABLE_FOR = 5; // polls with the same answer before it counts as finished (about 2.5 s)
const CEILING_MARGIN = 5 * 1000; // the whole turn's ceiling is the manifest's timeoutSeconds: end just before the host does

// One question at a time. Across invocations the lock is the only thing they share with the page.
const TURN_LOCK = "turn";
const QUEUE_LOCK = "closing";
const TABS_KEY = "tabs"; // lucode session id -> tab id
const BUSY_KEY = "busy_until"; // when the running turn must have ended
const CLOSING_KEY = "closing"; // sessions deleted during a turn: their tabs close after it

const LOST_NOTICE = "_The saved Google conversation could not be reopened (Google no longer has it for this browser profile), so this is a new one._\n\n";

const AI_MODE_NAMES = ["Modo IA", "AI Mode", "Modo de IA"];
const CONSENT_NAMES = ["Aceptar todo", "Accept all", "Acepto", "I agree"];
const INPUT_ROLES = ["textbox", "combobox", "searchbox"];

const TURN_COUNT_JS = `document.querySelectorAll('[data-scope-id="turn"]').length`;

const browser = lucode.browser;
const report = text => lucode.context.progress(text);
const sleep = ms => lucode.time.sleep(ms);
const now = () => lucode.time.now();
const messageOf = e => (e && e.message !== undefined ? String(e.message) : String(e));

function headless() {
  return lucode.settings.get("headless") === true;
}

function primer() {
  const own = String(lucode.settings.get("primer") || "").trim();
  return own || lucode.data.text("primer.md").trim();
}

// ---- the ceiling and stopping ---------------------------------------------------------------

const ceilingAt = now() + Math.max(0, lucode.context.deadlineMs - CEILING_MARGIN);

// What Go's ctx.Err() said: the turn was stopped, or reached its ceiling.
function stopReason() {
  if (lucode.context.cancelled()) return "context canceled";
  if (now() >= ceilingAt) return "context deadline exceeded";
  return "";
}

function checkStopped() {
  const reason = stopReason();
  if (reason) throw new Error(reason);
}

// ---- the session's tab ----------------------------------------------------------------------

const allTabs = () => lucode.memory.get(TABS_KEY) || {};
const saveTabs = tabs => lucode.memory.set(TABS_KEY, tabs);

function setTab(sessID, id) {
  const tabs = allTabs();
  tabs[sessID] = id;
  saveTabs(tabs);
}

function forgetTab(sessID) {
  const tabs = allTabs();
  delete tabs[sessID];
  saveTabs(tabs);
}

// closeSession closes the tab of a session (a new chat session starts a new conversation).
function closeSession(sessID) {
  const id = allTabs()[sessID];
  if (id === undefined) return;
  forgetTab(sessID);
  try { browser.closeTab(id); } catch (_) { /* it was closed already */ }
}

function isBusy() {
  const until = lucode.memory.get(BUSY_KEY);
  return typeof until === "number" && until > now();
}

// Never throws: it runs while a turn ends, and must not replace that turn's own error.
function closeQueued() {
  try {
    const queued = lucode.locks.with(QUEUE_LOCK, () => {
      const list = lucode.memory.get(CLOSING_KEY) || [];
      lucode.memory.delete(CLOSING_KEY);
      return list;
    });
    for (const id of queued) closeSession(id);
  } catch (_) { /* the next turn closes them */ }
}

// ---- finding things on the page -------------------------------------------------------------

function find(options) {
  try {
    return browser.find(options) || [];
  } catch (_) {
    return [];
  }
}

function findByNames(names, roles) {
  for (const name of names) {
    for (const role of roles) {
      const els = find({ role, name, exact: true, limit: 1 });
      if (els.length > 0) return els;
    }
  }
  return [];
}

// findInput finds the message box: "Buscar" before the first question, "Haz una pregunta" after.
function findInput() {
  for (const role of INPUT_ROLES) {
    const els = find({ role, limit: 1 });
    if (els.length > 0) return els;
  }
  return [];
}

function waitFind(ms, finder) {
  const deadline = now() + ms;
  for (;;) {
    const els = finder();
    if (els.length > 0) return els[0];
    if (now() > deadline || stopReason()) return null;
    sleep(POLL);
  }
}

function info() {
  try {
    return browser.info() || null;
  } catch (_) {
    return null;
  }
}

function turns() {
  try {
    const n = parseInt(browser.evalPage(TURN_COUNT_JS), 10);
    return isNaN(n) ? 0 : n;
  } catch (_) {
    return 0;
  }
}

// blocked explains a page that is not what we expect: a human check, usually.
function blocked(fallback) {
  const p = info();
  if (p && (String(p.url).indexOf("/sorry/") >= 0 || String(p.title).toLowerCase().indexOf("unusual traffic") >= 0)) {
    if (headless()) {
      return new Error("Google is asking to verify that you are human, and Edge is hidden (the plugin's Hide the Edge window setting). Turn that setting off so the window shows, solve the check there, and send the message again");
    }
    return new Error("Google is asking to verify that you are human. Solve the check in the Edge window lucode opened, then send the message again (the browser profile keeps the result)");
  }
  return new Error(fallback);
}

// ---- opening a conversation -----------------------------------------------------------------

// acceptConsent closes Google's cookie dialog if there is one (it appears in some regions).
function acceptConsent() {
  for (const name of CONSENT_NAMES) {
    const els = find({ role: "button", name, exact: true, limit: 1 });
    if (els.length > 0) {
      try { browser.click(els[0].ref); } catch (_) { /* the page goes on or it does not */ }
      sleep(1000);
      return;
    }
  }
}

// enterAIMode opens google.com and clicks the "Modo IA" / "AI Mode" button, with the direct AI
// Mode address as the fallback if the button is not there.
function enterAIMode() {
  browser.navigate("https://www.google.com/");
  acceptConsent();
  let entered = false;
  const el = waitFind(8000, () => findByNames(AI_MODE_NAMES, ["link", "button"]));
  if (el) {
    try {
      browser.click(el.ref);
    } catch (e) {
      throw new Error('clicking "' + el.name + '": ' + messageOf(e));
    }
    // The click does not always take the page there (Google's home has shown the button and done
    // nothing): AI Mode is on when its address is. If it is not, use the address.
    for (let i = 0; i < 8 && !entered && !stopReason(); i++) {
      sleep(POLL);
      const p = info();
      entered = !!p && String(p.url).indexOf("udm=50") >= 0;
    }
  }
  if (!entered) browser.navigate("https://www.google.com/search?udm=50");
  if (!waitFind(PAGE_WAIT, findInput)) {
    throw blocked("AI Mode did not show a message box (it may not be available for this account or region)");
  }
  // A plain Google search has a box too: typing the instructions there would run them as a search
  // and leave a dead tab. AI Mode is on only when its address is.
  const p = info();
  if (!p || String(p.url).indexOf("udm=50") < 0) {
    throw blocked("Google showed its plain search instead of AI Mode (it may be asking to verify you are human, or AI Mode is not available for this account or region)");
  }
}

// resume opens a saved conversation in the current tab and waits for it to load. A long
// conversation takes a while and shows its turns little by little, so it is loaded when it has as
// many turns as it had when it was saved. A url that no longer works (another browser profile, a
// conversation Google dropped) ends with fewer turns, or none, and the caller starts a new one.
function resume(url, savedTurns) {
  const want = savedTurns < 1 ? 1 : savedTurns;
  report("Reopening the saved conversation…");
  try {
    browser.navigate(url);
  } catch (_) {
    return false;
  }
  const deadline = now() + RESUME_WAIT;
  let last = -1;
  let still = 0;
  while (now() < deadline && !stopReason()) {
    const n = turns();
    if (n !== last) {
      last = n;
      still = 0;
      if (n > 0) report("Reopening the saved conversation… " + n + " of " + want + " turns loaded");
    } else {
      still++;
    }
    if (n >= want && still >= 3 && findInput().length > 0) return true;
    // it stopped loading with fewer turns than it had (or with none): it will not get any better
    if (still >= GIVE_UP_AFTER) return false;
    sleep(POLL);
  }
  return false;
}

// blankTab makes a tab ready to be pointed somewhere and makes it the current one.
function blankTab() {
  const tabs = browser.tabs();
  if (tabs.length === 1 && (tabs[0].url === "about:blank" || tabs[0].url === "") && Object.keys(allTabs()).length === 0) {
    browser.switchTab(tabs[0].id); // the blank tab Edge opened with
    return tabs[0].id;
  }
  browser.newTab("about:blank");
  for (const t of browser.tabs()) {
    if (t.current) return t.id;
  }
  throw new Error("could not open a tab");
}

function savedConversation(sessID) {
  try {
    const c = lucode.conversations.get(sessID);
    return c ? { url: c.url || "", turns: c.turns || 0 } : { url: "", turns: 0 };
  } catch (_) {
    return { url: "", turns: 0 };
  }
}

// openConversation points the browser at the session's tab. For a session without one it opens
// google.com and enters AI Mode. It says whether the conversation is fresh, and whether a saved
// one could not be reopened.
function openConversation(sessID, keep) {
  const known = allTabs()[sessID];
  if (known !== undefined) {
    try {
      browser.switchTab(known);
      return { fresh: false, lostSaved: false };
    } catch (_) {
      forgetTab(sessID); // the user closed it: start over
    }
  }
  const id = blankTab();
  let lostSaved = false;
  // The browser lost the tab (it was closed, or lucode restarted) but the conversation is in the
  // history: its url reopens it, with all its turns, in this browser profile.
  if (keep) {
    const saved = savedConversation(sessID);
    if (saved.url) {
      if (resume(saved.url, saved.turns)) {
        setTab(sessID, id);
        return { fresh: false, lostSaved: false };
      }
      checkStopped();
      // Google no longer has it (another profile, a dropped conversation): say so, then start over
      lostSaved = true;
    }
  }
  report("Starting a new conversation in AI Mode…");
  try {
    enterAIMode();
  } catch (e) {
    try { browser.closeTab(id); } catch (_) { /* nothing more can be done about it */ }
    throw e;
  }
  setTab(sessID, id);
  return { fresh: true, lostSaved };
}

// ---- talking to the page --------------------------------------------------------------------

// attach puts a picture into the message box the way a paste of an image does, and waits for the
// page to take it (it uploads it before the message can be sent).
function attach(box, img) {
  report("Attaching the image…");
  try {
    browser.click(box.ref);
  } catch (e) {
    throw new Error("focusing the message box: " + messageOf(e));
  }
  let out;
  try {
    const code = lucode.data.text("attach.js");
    out = browser.evalPage("(" + code + ")(" + JSON.stringify(img.base64) + "," + JSON.stringify(img.mediaType) + ")");
  } catch (e) {
    throw new Error("attaching the image: " + messageOf(e));
  }
  if (out !== "ok") throw new Error("attaching the image: " + out);
  // the page shows the picture as a chip named after the file once it has taken it; the message
  // cannot be sent before that
  const name = "image." + String(img.mediaType).replace(/^image\//, "").split("jpeg").join("jpg");
  const chip = waitFind(20000, () => find({ role: "button", name, exact: true, limit: 1 }));
  if (!chip) throw new Error("Google did not take the image");
  sleep(1000); // it uploads it in the background
}

// send types text into the conversation and waits for the answer to finish.
function send(text, img) {
  const before = turns();
  let box = waitFind(PAGE_WAIT, findInput);
  if (!box) throw blocked("the message box is not on the page");
  if (img) {
    attach(box, img);
    // the page draws the box again with the picture in it: the old reference is stale
    box = waitFind(PAGE_WAIT, findInput);
    if (!box) throw blocked("the message box is not on the page");
  }
  try {
    browser.type(box.ref, text, { submit: true });
  } catch (e) {
    throw new Error("typing the message: " + messageOf(e));
  }
  report("Waiting for Google's answer…");
  // a new turn appears when Google takes the message
  let deadline = now() + TURN_WAIT;
  while (turns() <= before) {
    checkStopped();
    if (now() > deadline) throw blocked("Google did not take the message");
    sleep(POLL);
  }
  // the answer is finished when it is there and stops changing
  deadline = now() + ANSWER_WAIT;
  const answerJS = lucode.data.text("markdown.js");
  let last = "";
  let same = 0;
  for (;;) {
    checkStopped();
    let raw = "";
    try { raw = String(browser.evalPage(answerJS)); } catch (_) { /* the next poll may do better */ }
    if (raw.trim() !== "" && raw === last) {
      same++;
      if (same >= STABLE_FOR) return clean(raw);
    } else {
      last = raw;
      same = 0;
    }
    if (now() > deadline) {
      if (last.trim() !== "") return clean(last); // better a late answer than none
      throw new Error("AI Mode did not answer in time");
    }
    sleep(POLL);
  }
}

// remember saves where the conversation of a session is, so it can be continued later.
function remember(sessID, firstText) {
  const p = info();
  if (!p || !p.url || String(p.url).indexOf("about:") === 0) return;
  try {
    lucode.conversations.save({ sessionId: sessID, url: p.url, title: title(firstText), turns: turns(), source: CONVERSATION_SOURCE });
  } catch (e) {
    lucode.log.warn("the conversation could not be saved:", messageOf(e));
  }
}

// ---- a whole question -----------------------------------------------------------------------

function askLocked(sessID, text, img, keep) {
  if (String(text).trim() === "") throw new Error("empty message");
  report("Opening Edge…");
  browser.launch({ headless: headless() });
  const opened = openConversation(sessID, keep);
  if (opened.fresh) { // tell it how to answer; its reply to that is not for the user
    report("Giving Gemini its instructions…");
    try {
      send(primer(), null);
    } catch (e) {
      throw new Error("priming the conversation: " + messageOf(e));
    }
  }
  const answer = send(text, img);
  if (keep) remember(sessID, text);
  return opened.lostSaved ? LOST_NOTICE + answer : answer;
}

// One question at a time, across every conversation. A turn that never ends must not wedge the
// others: each has a ceiling, the manifest's timeoutSeconds, and one still waiting can be stopped.
function ask(sessID, text, img, options) {
  if (isBusy()) report("Waiting for another Gemini conversation to finish…");
  try {
    return lucode.locks.with(TURN_LOCK, () => {
      lucode.memory.set(BUSY_KEY, now() + lucode.context.deadlineMs);
      try {
        closeQueued();
        const answer = askLocked(sessID, text, img, options.keep);
        return answer;
      } finally {
        if (options.closeAfter) closeSession(sessID);
        lucode.memory.delete(BUSY_KEY);
        closeQueued();
      }
    });
  } catch (e) {
    if (/while waiting for the lock/.test(messageOf(e))) {
      throw new Error("gave up waiting for another Gemini conversation to finish: " + (stopReason() || "context canceled"));
    }
    throw e;
  }
}

// What session/aimode.go did with an error: the same words after "Google AI Mode: ". A string is
// thrown so that the host shows exactly this text.
function publicError(e) {
  return ERROR_PREFIX + messageOf(e);
}

// ---- what the host calls --------------------------------------------------------------------

function turn(req) {
  try {
    return ask(req.sessionId, req.message, req.image || null, { keep: true, closeAfter: false });
  } catch (e) {
    throw publicError(e);
  }
}

// A flow has no chat to come back to: a new conversation, closed when it has answered.
function askNode(input) {
  const run = input.run && input.run.id ? input.run.id : String(now());
  const sessID = "flow:" + run + ":" + now();
  try {
    return { port: "next", output: { answer: ask(sessID, input.config.question, null, { keep: false, closeAfter: true }) } };
  } catch (e) {
    throw publicError(e);
  }
}

// A chat that is gone (deleted, or replaced by a new Supervisor conversation) closes its tab. The
// handler's 10 seconds cannot wait out a running turn, so that turn closes it.
function sessionClosed(e) {
  const sessID = e && e.sessionId;
  if (!sessID) return;
  if (isBusy()) {
    lucode.locks.with(QUEUE_LOCK, () => {
      const list = lucode.memory.get(CLOSING_KEY) || [];
      list.push(sessID);
      lucode.memory.set(CLOSING_KEY, list);
    });
    return;
  }
  lucode.locks.with(TURN_LOCK, () => closeSession(sessID));
}

module.exports = {
  models: { aimode: { turn } },
  nodes: { ask: askNode },
  events: { sessionClosed },
};
