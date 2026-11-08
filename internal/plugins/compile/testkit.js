// The globals of a test file of lucode-plugin test: test() and assert. `plugin` comes from Go.
(function (g) {
  "use strict";
  var tests = [];
  function test(name, fn) { tests.push({ name: String(name), fn: fn }); }
  test.only = function (name, fn) { tests.push({ name: String(name), fn: fn, only: true }); };
  test.skip = function (name) { tests.push({ name: String(name), skip: true }); };

  function show(v) {
    if (v === undefined) return "undefined";
    if (typeof v === "string") return JSON.stringify(v);
    try { return JSON.stringify(v); } catch (e) { return String(v); }
  }
  function fail(msg, fallback) { throw new Error(msg || fallback); }
  function same(a, b) {
    if (a === b) return true;
    if (typeof a !== "object" || typeof b !== "object" || a === null || b === null) return a !== a && b !== b;
    if (Array.isArray(a) !== Array.isArray(b)) return false;
    var ka = Object.keys(a), kb = Object.keys(b);
    if (ka.length !== kb.length) return false;
    for (var i = 0; i < ka.length; i++) {
      if (!Object.prototype.hasOwnProperty.call(b, ka[i]) || !same(a[ka[i]], b[ka[i]])) return false;
    }
    return true;
  }

  function assert(value, msg) { if (!value) fail(msg, "expected a true value, got " + show(value)); }
  assert.ok = assert;
  assert.equal = function (actual, expected, msg) {
    if (actual !== expected) fail(msg, "expected " + show(expected) + ", got " + show(actual));
  };
  assert.notEqual = function (actual, unexpected, msg) {
    if (actual === unexpected) fail(msg, "expected anything but " + show(unexpected));
  };
  assert.deepEqual = function (actual, expected, msg) {
    if (!same(actual, expected)) fail(msg, "expected " + show(expected) + "\n         got " + show(actual));
  };
  assert.includes = function (haystack, needle, msg) {
    var ok = typeof haystack === "string" ? haystack.indexOf(String(needle)) >= 0
      : Array.isArray(haystack) && haystack.some(function (x) { return same(x, needle); });
    if (!ok) fail(msg, "expected " + show(haystack) + " to include " + show(needle));
  };
  assert.match = function (text, re, msg) {
    if (!re.test(String(text))) fail(msg, "expected " + show(text) + " to match " + String(re));
  };
  assert.throws = function (fn, expected, msg) {
    try {
      fn();
    } catch (e) {
      var text = e && e.message !== undefined ? String(e.message) : String(e);
      if (expected instanceof RegExp && !expected.test(text)) fail(msg, "it threw " + show(text) + ", which does not match " + String(expected));
      if (typeof expected === "string" && text.indexOf(expected) < 0) fail(msg, "it threw " + show(text) + ", without " + show(expected));
      return e;
    }
    fail(msg, "expected it to throw");
  };
  assert.fail = function (msg) { fail(msg, "failed"); };

  g.test = test;
  g.assert = assert;
  g.__lucodeTests = tests;
})(this);
