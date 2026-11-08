// Helpers lucode-plugin test adds to a page under test, after the plugin's script. They read the
// page's own document, the one the Worker runtime keeps; actions go through the same messages the
// browser sends.
(function (g) {
  "use strict";
  var doc = g.document;
  var VOID = { area: 1, br: 1, col: 1, hr: 1, img: 1, input: 1, wbr: 1 };

  function norm(s) { return String(s).replace(/\s+/g, " ").trim(); }

  function all(sel) {
    sel = String(sel);
    if (sel.indexOf("text=") === 0) {
      var want = norm(sel.slice(5));
      var out = [];
      (function walk(n) {
        for (var i = 0; i < n.childNodes.length; i++) {
          var c = n.childNodes[i];
          if (c.nodeType !== 1) continue;
          walk(c);
          var inner = out.some(function (o) { return c.contains(o); });
          if (!inner && norm(c.textContent) === want) out.push(c);
        }
      })(doc.body);
      return out;
    }
    return doc.querySelectorAll(sel);
  }
  function one(sel) {
    var list = all(sel);
    if (!list.length) throw new Error("nothing on the page matches " + sel);
    return list[0];
  }
  function byId(id) {
    var found = null;
    (function walk(n) {
      if (n.__id === id) { found = n; return; }
      for (var i = 0; i < n.childNodes.length && !found; i++) walk(n.childNodes[i]);
    })(doc.body);
    return found;
  }
  function esc(s, attr) {
    s = String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
    return attr ? s.replace(/"/g, "&quot;") : s;
  }
  function html(n) {
    if (n.nodeType === 3) return esc(n.data);
    if (n.nodeType !== 1) return "";
    var out = "<" + n.localName;
    n.attributes.forEach(function (a) { out += " " + a.name + '="' + esc(a.value, true) + '"'; });
    var st = n.style.cssText;
    if (st) out += ' style="' + esc(st, true) + '"';
    if (n.__value !== undefined && !n.hasAttribute("value")) out += ' value="' + esc(n.__value, true) + '"';
    if (n.__checked === true && !n.hasAttribute("checked")) out += " checked";
    out += ">";
    if (VOID[n.localName]) return out;
    return out + n.childNodes.map(html).join("") + "</" + n.localName + ">";
  }
  function disabled(n) {
    for (; n && n.nodeType === 1; n = n.parentNode) {
      if ((n.localName === "button" || n.localName === "input" || n.localName === "select" ||
        n.localName === "textarea" || n.localName === "fieldset") && n.hasAttribute("disabled")) return true;
    }
    return false;
  }

  g.__lucodeTest = {
    find: function (sel) { return one(sel).__id; },
    count: function (sel) { return all(sel).length; },
    html: function (sel) { return sel ? html(one(sel)) : doc.body.childNodes.map(html).join(""); },
    text: function (sel) { return norm((sel ? one(sel) : doc.body).textContent); },
    attr: function (sel, name) { return one(sel).getAttribute(name); },
    value: function (sel) { return one(sel).value; },
    checked: function (sel) { return !!one(sel).checked; },
    disabled: function (sel) { return disabled(one(sel)); },
    isDisabled: function (id) { var n = byId(id); return !!n && disabled(n); },
    // a click on a submit button submits its form, as the browser does
    submitFor: function (id) {
      var n = byId(id), b = null;
      for (; n && n.nodeType === 1; n = n.parentNode) {
        if (n.localName === "button" || (n.localName === "input" && /^(submit|image)$/i.test(n.getAttribute("type") || ""))) { b = n; break; }
      }
      if (!b) return 0;
      if (b.localName === "button" && (b.getAttribute("type") || "submit").toLowerCase() !== "submit") return 0;
      for (var f = b.parentNode; f; f = f.parentNode) if (f.localName === "form") return f.__id;
      return 0;
    },
  };
})(this);
