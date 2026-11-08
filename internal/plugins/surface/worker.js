// The start of every plugin frontend: it runs first in the plugin's own Worker, then the plugin's
// bundle runs after it. The Worker never sees the page: what the plugin builds here is a small
// document of its own, and every change to it goes to the page as an operation the page checks
// against its own lists before it draws anything. Nothing here is a security boundary: the page
// treats every message from the Worker as untrusted, and the Worker's response carries a
// Content-Security-Policy that cuts it off from the network. What this file removes below is the
// same-origin state a Worker would otherwise reach (storage, caches, other tabs).
(function (global, context) {
  "use strict";

  var post = global.postMessage.bind(global);
  var listen = global.addEventListener.bind(global);
  var microtask = typeof global.queueMicrotask === "function"
    ? global.queueMicrotask.bind(global)
    : function (fn) { Promise.resolve().then(fn); };

  // ---- what a plugin must not reach --------------------------------------------------------

  function cut(owner, name) {
    for (var o = owner; o; o = Object.getPrototypeOf(o)) {
      if (Object.prototype.hasOwnProperty.call(o, name)) {
        try { delete o[name]; } catch (e) { /* not configurable */ }
        if (Object.prototype.hasOwnProperty.call(o, name)) {
          try { Object.defineProperty(o, name, { value: undefined, configurable: false, writable: false }); } catch (e) { /* frozen */ }
        }
      }
    }
  }
  [
    "fetch", "XMLHttpRequest", "WebSocket", "WebSocketStream", "WebTransport", "EventSource",
    "importScripts", "Worker", "SharedWorker", "BroadcastChannel", "indexedDB", "IDBFactory",
    "caches", "CacheStorage", "Cache", "cookieStore", "CookieStore", "FileSystemHandle",
    "FileSystemDirectoryHandle", "FileSystemFileHandle", "FileSystemSyncAccessHandle", "StorageManager",
    "Notification", "ServiceWorker", "ServiceWorkerContainer", "ServiceWorkerRegistration",
    "PushManager", "LockManager", "BackgroundFetchManager", "Request", "Response",
  ].forEach(function (name) { cut(global, name); });
  if (global.navigator) {
    ["storage", "locks", "serviceWorker", "sendBeacon", "permissions", "usb", "hid", "serial",
      "bluetooth", "connection", "mediaCapabilities"].forEach(function (name) { cut(global.navigator, name); });
  }

  // ---- the document ------------------------------------------------------------------------

  var HTML = "http://www.w3.org/1999/xhtml";
  var SVG = "http://www.w3.org/2000/svg";
  var MATH = "http://www.w3.org/1998/Math/MathML";
  var MAX_NODES = 50000;

  var ops = [];
  var flushing = false;
  var nextId = 1;
  var live = new Map(); // id -> node, for the nodes the page has
  var listened = Object.create(null); // event types some node listens to

  function emit(op) {
    ops.push(op);
    if (!flushing) {
      flushing = true;
      microtask(flush);
    }
  }
  function flush() {
    flushing = false;
    if (ops.length) post({ lucode: 1, t: "ops", ops: ops.splice(0, ops.length) });
  }

  function kebab(name) {
    if (name.indexOf("-") === 0) return name; // a custom property, or a vendor prefix as written
    return name.replace(/[A-Z]/g, function (c) { return "-" + c.toLowerCase(); });
  }

  function makeStyle(el) {
    var values = Object.create(null);
    function set(prop, value) {
      prop = kebab(String(prop));
      value = value == null ? "" : String(value);
      if (value === "") delete values[prop];
      else values[prop] = value;
      if (el.__live) emit(["style", el.__id, prop, value === "" ? null : value]);
    }
    var api = {
      setProperty: function (prop, value) { set(prop, value); },
      removeProperty: function (prop) { set(prop, ""); },
      getPropertyValue: function (prop) { return values[kebab(String(prop))] || ""; },
    };
    Object.defineProperty(api, "cssText", {
      get: function () {
        return Object.keys(values).map(function (k) { return k + ": " + values[k]; }).join("; ");
      },
      set: function (text) {
        Object.keys(values).forEach(function (k) { set(k, ""); });
        String(text == null ? "" : text).split(";").forEach(function (decl) {
          var at = decl.indexOf(":");
          if (at > 0) set(decl.slice(0, at).trim(), decl.slice(at + 1).trim());
        });
      },
    });
    el.__styles = values;
    return new Proxy(api, {
      get: function (target, prop) {
        if (typeof prop !== "string" || prop in target) return target[prop];
        return values[kebab(prop)] || "";
      },
      set: function (target, prop, value) {
        if (prop === "cssText") target.cssText = value;
        else if (typeof prop === "string") set(prop, value);
        return true;
      },
    });
  }

  function Node(type) {
    this.nodeType = type;
    this.parentNode = null;
    this.childNodes = [];
    this.__id = nextId++;
    this.__live = false;
  }
  Node.prototype = {
    get ownerDocument() { return document; },
    get firstChild() { return this.childNodes[0] || null; },
    get lastChild() { return this.childNodes[this.childNodes.length - 1] || null; },
    get parentElement() { return this.parentNode && this.parentNode.nodeType === 1 ? this.parentNode : null; },
    get nextSibling() {
      var p = this.parentNode;
      if (!p) return null;
      var i = p.childNodes.indexOf(this);
      return p.childNodes[i + 1] || null;
    },
    get previousSibling() {
      var p = this.parentNode;
      if (!p) return null;
      var i = p.childNodes.indexOf(this);
      return i > 0 ? p.childNodes[i - 1] : null;
    },
    get isConnected() { return this.__live; },
    hasChildNodes: function () { return this.childNodes.length > 0; },
    contains: function (other) {
      for (var n = other; n; n = n.parentNode) if (n === this) return true;
      return false;
    },
    insertBefore: function (child, ref) {
      if (!(child instanceof Node)) throw new TypeError("insertBefore: not a node of this document");
      if (child.nodeType === 11) { // a fragment: its children, in order
        var kids = child.childNodes.slice();
        for (var k = 0; k < kids.length; k++) this.insertBefore(kids[k], ref);
        return child;
      }
      if (child.contains(this)) throw new Error("insertBefore: a node cannot hold itself");
      if (ref && ref.parentNode !== this) throw new Error("insertBefore: the reference is not a child of this node");
      if (child === ref) return child;
      var old = child.parentNode;
      if (old) {
        old.childNodes.splice(old.childNodes.indexOf(child), 1);
        child.parentNode = null;
        if (child.__live && !this.__live) unmount(child, true);
      }
      var at = ref ? this.childNodes.indexOf(ref) : this.childNodes.length;
      this.childNodes.splice(at, 0, child);
      child.parentNode = this;
      if (this.__live) {
        if (!child.__live) mount(child);
        emit(["insert", this.__id, child.__id, ref ? ref.__id : 0]);
      }
      return child;
    },
    appendChild: function (child) { return this.insertBefore(child, null); },
    removeChild: function (child) {
      if (child.parentNode !== this) throw new Error("removeChild: not a child of this node");
      this.childNodes.splice(this.childNodes.indexOf(child), 1);
      child.parentNode = null;
      if (child.__live) unmount(child, true);
      return child;
    },
    replaceChild: function (next, old) {
      this.insertBefore(next, old);
      return this.removeChild(old);
    },
    remove: function () { if (this.parentNode) this.parentNode.removeChild(this); },
    append: function () { for (var i = 0; i < arguments.length; i++) this.appendChild(toNode(arguments[i])); },
    get textContent() {
      if (this.nodeType === 3 || this.nodeType === 8) return this.__data;
      return this.childNodes.map(function (c) { return c.nodeType === 8 ? "" : c.textContent; }).join("");
    },
    set textContent(text) {
      if (this.nodeType === 3 || this.nodeType === 8) { this.data = text; return; }
      while (this.childNodes.length) this.removeChild(this.childNodes[0]);
      text = text == null ? "" : String(text);
      if (text) this.appendChild(document.createTextNode(text));
    },
  };

  function toNode(v) { return v instanceof Node ? v : document.createTextNode(String(v)); }

  // mount sends a node and what is under it to the page, the first time it is attached
  function mount(node) {
    if (live.size >= MAX_NODES) throw new Error("this page has more than " + MAX_NODES + " elements");
    node.__live = true;
    live.set(node.__id, node);
    if (node.nodeType === 3) emit(["text", node.__id, node.__data]);
    else if (node.nodeType === 8) emit(["text", node.__id, ""]);
    else {
      emit(["el", node.__id, node.localName, node.namespaceURI === SVG ? 1 : node.namespaceURI === MATH ? 2 : 0]);
      node.__attrs.forEach(function (v, k) { emit(["attr", node.__id, k, v]); });
      Object.keys(node.__styles).forEach(function (k) { emit(["style", node.__id, k, node.__styles[k]]); });
      for (var i = 0; i < node.childNodes.length; i++) {
        var c = node.childNodes[i];
        mount(c);
        emit(["insert", node.__id, c.__id, 0]);
      }
      // after the children: a <select> takes its value from the options it already has
      if (node.__value !== undefined) emit(["prop", node.__id, "value", node.__value]);
      if (node.__checked !== undefined) emit(["prop", node.__id, "checked", node.__checked]);
      if (node.__selected !== undefined) emit(["prop", node.__id, "selected", node.__selected]);
    }
  }
  // unmount: the page forgets the node and its children, which may come back later as new ones
  function unmount(node, top) {
    if (top) emit(["remove", node.__id]);
    node.__live = false;
    live.delete(node.__id);
    for (var i = 0; i < node.childNodes.length; i++) unmount(node.childNodes[i], false);
  }

  function CharacterData(type, data) {
    Node.call(this, type);
    this.__data = String(data);
  }
  CharacterData.prototype = Object.create(Node.prototype, {
    data: {
      get: function () { return this.__data; },
      set: function (v) {
        v = v == null ? "" : String(v);
        if (v === this.__data) return;
        this.__data = v;
        if (this.__live && this.nodeType === 3) emit(["data", this.__id, v]);
      },
    },
    nodeValue: {
      get: function () { return this.__data; },
      set: function (v) { this.data = v; },
    },
    nodeName: { get: function () { return this.nodeType === 3 ? "#text" : "#comment"; } },
  });

  function Element(ns, name) {
    Node.call(this, 1);
    this.namespaceURI = ns;
    this.localName = ns === HTML ? String(name).toLowerCase() : String(name);
    this.__attrs = new Map();
    this.__listeners = Object.create(null);
    this.style = makeStyle(this);
  }
  function attrSetter(name) {
    return {
      get: function () { return this.getAttribute(name) || ""; },
      set: function (v) { this.setAttribute(name, v); },
    };
  }
  function propAccessor(name) {
    var slot = "__" + name;
    return {
      get: function () {
        if (this[slot] !== undefined) return this[slot];
        if (name === "value") return this.getAttribute("value") || (this.localName === "textarea" ? this.textContent : "");
        return this.hasAttribute(name);
      },
      set: function (v) {
        v = name === "value" ? (v == null ? "" : String(v)) : !!v;
        this[slot] = v;
        if (this.__live) emit(["prop", this.__id, name, v]);
      },
    };
  }
  Element.prototype = Object.create(Node.prototype, {
    nodeName: { get: function () { return this.namespaceURI === HTML ? this.localName.toUpperCase() : this.localName; } },
    tagName: { get: function () { return this.nodeName; } },
    className: attrSetter("class"),
    htmlFor: attrSetter("for"),
    id: attrSetter("id"),
    value: propAccessor("value"),
    checked: propAccessor("checked"),
    selected: propAccessor("selected"),
    attributes: {
      get: function () {
        var out = [];
        this.__attrs.forEach(function (value, name) { out.push({ name: name, value: value }); });
        return out;
      },
    },
    children: { get: function () { return this.childNodes.filter(function (c) { return c.nodeType === 1; }); } },
    innerHTML: {
      get: function () { return ""; },
      set: function () { throw new Error("innerHTML is not available in a plugin page: build the elements instead"); },
    },
    outerHTML: { get: function () { return ""; } },
  });
  Element.prototype.setAttribute = function (name, value) {
    name = String(name);
    value = String(value);
    if (this.__attrs.get(name) === value) return;
    this.__attrs.set(name, value);
    if (this.__live) emit(["attr", this.__id, name, value]);
  };
  Element.prototype.getAttribute = function (name) {
    var v = this.__attrs.get(String(name));
    return v === undefined ? null : v;
  };
  Element.prototype.hasAttribute = function (name) { return this.__attrs.has(String(name)); };
  Element.prototype.removeAttribute = function (name) {
    name = String(name);
    if (!this.__attrs.has(name)) return;
    this.__attrs.delete(name);
    if (this.__live) emit(["attr", this.__id, name, null]);
  };
  Element.prototype.toggleAttribute = function (name, force) {
    var on = force === undefined ? !this.hasAttribute(name) : !!force;
    if (on) this.setAttribute(name, "");
    else this.removeAttribute(name);
    return on;
  };
  Element.prototype.addEventListener = function (type, fn, opts) {
    if (typeof fn !== "function" && !(fn && typeof fn.handleEvent === "function")) return;
    var capture = typeof opts === "boolean" ? opts : !!(opts && opts.capture);
    var key = type + (capture ? ":c" : ":b");
    var list = this.__listeners[key] || (this.__listeners[key] = []);
    if (list.indexOf(fn) >= 0) return;
    list.push(fn);
    if (!listened[type]) {
      listened[type] = true;
      emit(["listen", String(type)]);
    }
  };
  Element.prototype.removeEventListener = function (type, fn, opts) {
    var capture = typeof opts === "boolean" ? opts : !!(opts && opts.capture);
    var list = this.__listeners[type + (capture ? ":c" : ":b")];
    if (list && list.indexOf(fn) >= 0) list.splice(list.indexOf(fn), 1);
  };
  Element.prototype.focus = function () { if (this.__live) emit(["focus", this.__id]); };
  Element.prototype.blur = function () { if (this.__live) emit(["blur", this.__id]); };
  Element.prototype.click = function () { dispatch(this, "click", {}); };
  Element.prototype.getBoundingClientRect = function () {
    return { x: 0, y: 0, top: 0, left: 0, right: 0, bottom: 0, width: 0, height: 0 };
  };
  Element.prototype.querySelector = function (sel) { return query(this, sel, true)[0] || null; };
  Element.prototype.querySelectorAll = function (sel) { return query(this, sel, false); };

  function Fragment() { Node.call(this, 11); }
  Fragment.prototype = Object.create(Node.prototype, { nodeName: { value: "#document-fragment" } });

  // a small subset of selectors: tag, #id, .class, [attr] and [attr=value], joined by spaces
  function matches(el, part) {
    var re = /([#.]?[\w-]+)|\[([\w-]+)(?:=["']?([^\]"']*)["']?)?\]/g;
    var m, any = false;
    while ((m = re.exec(part))) {
      any = true;
      if (m[1]) {
        var t = m[1];
        if (t[0] === "#") { if (el.getAttribute("id") !== t.slice(1)) return false; }
        else if (t[0] === ".") { if ((" " + (el.getAttribute("class") || "") + " ").indexOf(" " + t.slice(1) + " ") < 0) return false; }
        else if (el.localName !== t.toLowerCase()) return false;
      } else if (m[3] !== undefined) { if (el.getAttribute(m[2]) !== m[3]) return false; }
      else if (!el.hasAttribute(m[2])) return false;
    }
    return any;
  }
  function query(root, sel, first) {
    var parts = String(sel).trim().split(/\s+/);
    var out = [];
    (function walk(n, anc) {
      for (var i = 0; i < n.childNodes.length; i++) {
        var c = n.childNodes[i];
        if (c.nodeType !== 1) continue;
        var chain = anc.concat([c]);
        if (matches(c, parts[parts.length - 1])) {
          var j = parts.length - 2;
          for (var k = chain.length - 2; k >= 0 && j >= 0; k--) if (matches(chain[k], parts[j])) j--;
          if (j < 0) {
            out.push(c);
            if (first) return true;
          }
        }
        if (walk(c, chain) && first) return true;
      }
      return false;
    })(root, []);
    return out;
  }

  var body = new Element(HTML, "body");
  body.__id = 0;
  body.__live = true;
  live.set(0, body);

  var document = {
    nodeType: 9,
    body: body,
    documentElement: body,
    defaultView: global,
    createElement: function (name) { return new Element(HTML, name); },
    createElementNS: function (ns, name) { return new Element(ns || HTML, name); },
    createTextNode: function (text) { return new CharacterData(3, text == null ? "" : text); },
    createComment: function (text) { return new CharacterData(8, text == null ? "" : text); },
    createDocumentFragment: function () { return new Fragment(); },
    getElementById: function (id) { return query(body, "#" + id, true)[0] || null; },
    querySelector: function (sel) { return body.querySelector(sel); },
    querySelectorAll: function (sel) { return body.querySelectorAll(sel); },
    addEventListener: function () {},
    removeEventListener: function () {},
  };
  Object.defineProperty(document, "activeElement", { get: function () { return focused || body; } });

  global.document = document;
  global.Node = Node;
  global.Element = Element;
  global.HTMLElement = Element;
  global.SVGElement = Element;
  global.Text = CharacterData;
  global.DocumentFragment = Fragment;
  global.window = global;

  // ---- events from the page ----------------------------------------------------------------

  var NON_BUBBLING = { focus: 1, blur: 1, mouseenter: 1, mouseleave: 1, pointerenter: 1, pointerleave: 1, scroll: 1, toggle: 1 };
  var focused = null;

  function dispatch(target, type, detail) {
    var path = [];
    for (var n = target; n; n = n.parentNode) path.push(n);
    var state = { stop: false, now: false };
    var ev = {
      type: type,
      target: target,
      currentTarget: null,
      bubbles: !NON_BUBBLING[type],
      cancelable: true,
      defaultPrevented: false,
      isTrusted: true,
      timeStamp: Date.now(),
      eventPhase: 0,
      preventDefault: function () { ev.defaultPrevented = true; },
      stopPropagation: function () { state.stop = true; },
      stopImmediatePropagation: function () { state.stop = true; state.now = true; },
      composedPath: function () { return path.slice(); },
    };
    var keys = Object.keys(detail || {});
    for (var k = 0; k < keys.length; k++) if (!(keys[k] in ev)) ev[keys[k]] = detail[keys[k]];

    function run(node, phase, key) {
      var list = node.__listeners && node.__listeners[key];
      if (!list || !list.length) return;
      ev.currentTarget = node;
      ev.eventPhase = phase;
      var copy = list.slice();
      for (var i = 0; i < copy.length && !state.now; i++) {
        try {
          if (typeof copy[i] === "function") copy[i].call(node, ev);
          else copy[i].handleEvent(ev);
        } catch (e) {
          report(e);
        }
      }
    }
    for (var c = path.length - 1; c >= 1 && !state.stop; c--) run(path[c], 1, type + ":c");
    if (!state.stop) { run(target, 2, type + ":c"); run(target, 2, type + ":b"); }
    if (ev.bubbles) for (var b = 1; b < path.length && !state.stop; b++) run(path[b], 3, type + ":b");
    ev.currentTarget = null;
    return ev.defaultPrevented;
  }

  function report(e) {
    var msg = e && e.message ? String(e.message) : String(e);
    try { post({ lucode: 1, t: "error", message: msg.slice(0, 2000) }); } catch (x) { /* not cloneable */ }
  }
  listen("error", function (e) { report(e.error || e.message); });
  listen("unhandledrejection", function (e) { report(e.reason); });

  // ---- the bridge to the page --------------------------------------------------------------

  var waiting = new Map();
  var rpcSeq = 0;
  var handlers = Object.create(null);

  function plainArgs(args) {
    if (args === undefined || args === null) return {};
    return JSON.parse(JSON.stringify(args));
  }

  listen("message", function (e) {
    var m = e.data;
    if (!m || m.lucode !== 1) return;
    switch (m.t) {
      case "ev": {
        var target = live.get(m.target);
        if (!target) return;
        if (m.sync) {
          if (typeof m.sync.value === "string") target.__value = m.sync.value;
          if (typeof m.sync.checked === "boolean") target.__checked = m.sync.checked;
        }
        if (m.type === "focus" || m.type === "focusin") focused = target;
        if ((m.type === "blur" || m.type === "focusout") && focused === target) focused = null;
        var prevented = dispatch(target, m.type, m.detail);
        post({ lucode: 1, t: "evdone", seq: m.seq, prevented: prevented });
        break;
      }
      case "rpcres": {
        var w = waiting.get(m.id);
        if (!w) return;
        waiting.delete(m.id);
        if (m.ok) w.resolve(m.value);
        else w.reject(new Error(String(m.error || "the call failed")));
        break;
      }
      case "event": {
        var list = handlers[m.name] || [];
        for (var i = 0; i < list.length; i++) {
          try { list[i](m.data); } catch (x) { report(x); }
        }
        break;
      }
    }
  });

  var lucode = {
    /** Who this page is for: plugin, page and project. */
    context: Object.freeze(context),
    /** Calls an rpc of this plugin that the page lists in plugin.json. */
    rpc: function (name, args) {
      var id = ++rpcSeq;
      var payload;
      try {
        payload = plainArgs(args);
      } catch (e) {
        return Promise.reject(new Error("the arguments of an rpc must be plain JSON"));
      }
      return new Promise(function (resolve, reject) {
        waiting.set(id, { resolve: resolve, reject: reject });
        post({ lucode: 1, t: "rpc", id: id, name: String(name), args: payload });
      });
    },
    /** A short message in the app's own toasts, with the plugin's name on it. */
    toast: function (message, tone) {
      post({ lucode: 1, t: "toast", message: String(message), tone: tone ? String(tone) : "default" });
    },
    /** Opens an http or https address in a new tab. */
    openLink: function (url) { post({ lucode: 1, t: "open", url: String(url) }); },
    /** An image of the zip, for an <img src>: lucode.asset("assets/chart.png"). */
    asset: function (path) { return "lucode-asset:" + String(path); },
    /** Things the app tells the page: "webhook" (one reached this plugin), "theme" ("dark" or "light"). */
    on: function (name, fn) {
      (handlers[name] || (handlers[name] = [])).push(fn);
      return function () {
        var list = handlers[name] || [];
        if (list.indexOf(fn) >= 0) list.splice(list.indexOf(fn), 1);
      };
    },
  };
  Object.freeze(lucode);
  Object.defineProperty(global, "lucode", { value: lucode, enumerable: true });

  post({ lucode: 1, t: "ready" });
})(self, __LUCODE_CONTEXT__);
