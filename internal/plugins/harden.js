// Runs in every fresh VM before any plugin code. The script engine checks for an interrupt between
// instructions, never inside a native function, and a native call the deadline gave up on goes on
// allocating. So what could run text as code goes away, and every native that can allocate or loop
// without end from a small input is reached through a size check here. What loops over a value and
// could not be checked first (iterators, join, JSON) is written in script, where interrupts work.
(function () {
  "use strict";
  return function (jsonDepthOK) {
    var apply = Reflect.apply;
    var construct = Reflect.construct;
    var names = Object.getOwnPropertyNames;
    var keys = Object.keys;
    var isArray = Array.isArray;
    var nativeStringify = JSON.stringify;
    var nativeParse = JSON.parse;
    var define = Object.defineProperty;
    var describe = Object.getOwnPropertyDescriptor;
    var getProto = Object.getPrototypeOf;
    var toStr = String;
    var SymbolIterator = Symbol.iterator;

    var MAX_LENGTH = 1 << 20; // elements a native may work on or make
    var MAX_STRING = 1 << 25; // characters a native may work on or make
    var MAX_REGEX_INPUT = 1 << 22; // a global match makes an element per match
    var MAX_ARGS = 1 << 16; // arguments of one call
    var MAX_REGEX_SOURCE = 1 << 16;

    function tooLong() { throw new RangeError("this array is too long for the plugin runtime"); }
    function tooLongText() { throw new RangeError("this string is too long for the plugin runtime"); }

    // the constructors of function kinds are eval by another name
    var protos = [
      getProto(function () {}),
      getProto(function* () {}),
      getProto(async function () {}),
    ];
    for (var i = 0; i < protos.length; i++) delete protos[i].constructor;

    var banned = [
      "eval", "Function", "Proxy", "WeakRef", "FinalizationRegistry", "BigInt",
      "ArrayBuffer", "SharedArrayBuffer", "DataView", "Atomics",
      "Int8Array", "Uint8Array", "Uint8ClampedArray", "Int16Array", "Uint16Array",
      "Int32Array", "Uint32Array", "Float16Array", "Float32Array", "Float64Array",
      "BigInt64Array", "BigUint64Array",
    ];
    for (var b = 0; b < banned.length; b++) delete globalThis[banned[b]];

    function hidden(obj, name, fn) {
      define(obj, name, { value: fn, writable: true, enumerable: false, configurable: true });
    }

    // The length a native will read. It reads it itself right after this check, so a length that
    // could answer differently the second time (a getter, an object with valueOf) is refused.
    function checkLength(o, limit) {
      if (o === null || o === undefined) return;
      if (typeof o === "string") {
        if (o.length > limit) tooLong();
        return;
      }
      if (typeof o !== "object" && typeof o !== "function") return;
      if (isArray(o)) {
        if (o.length > limit) tooLong();
        return;
      }
      for (var p = o; p !== null; p = getProto(p)) {
        var d = describe(p, "length");
        if (d) {
          if (!("value" in d) || (typeof d.value !== "number" && d.value !== undefined)) {
            throw new TypeError("the length of this value must be a plain number");
          }
          if (d.value > limit) tooLong();
          return;
        }
      }
    }

    // ---- calling ------------------------------------------------------------------------------

    hidden(protos[0], "apply", function (thisArg, args) {
      if (args === null || args === undefined) return apply(this, thisArg, []);
      checkLength(args, MAX_ARGS);
      return apply(this, thisArg, args);
    });
    hidden(Reflect, "apply", function (fn, thisArg, args) {
      checkLength(args, MAX_ARGS);
      return apply(fn, thisArg, args);
    });
    hidden(Reflect, "construct", function (fn, args, target) {
      checkLength(args, MAX_ARGS);
      return arguments.length > 2 ? construct(fn, args, target) : construct(fn, args);
    });

    // ---- iterating ----------------------------------------------------------------------------

    // Spread, for-of, new Map(x), Array.from(x) and the rest ask the iterator for one value at a
    // time from a native loop. With next written in script, each step can be interrupted.
    function iterator(next) {
      var it = { next: next };
      define(it, SymbolIterator, { value: function () { return this; }, enumerable: false });
      return it;
    }
    function arrayIterator(o, make) {
      var at = 0, done = false;
      return iterator(function () {
        if (!done && at < o.length) return { value: make(at++), done: false };
        done = true;
        return { value: undefined, done: true };
      });
    }

    var AP = Array.prototype;
    var values = function () {
      var o = Object(this);
      return arrayIterator(o, function (i) { return o[i]; });
    };

    // natives that loop without checking for an interrupt get a size check first
    names(AP).forEach(function (name) {
      var orig = AP[name];
      if (name === "constructor" || typeof orig !== "function") return;
      hidden(AP, name, function () {
        checkLength(this, MAX_LENGTH);
        return apply(orig, this, arguments);
      });
    });
    hidden(AP, "values", values);
    hidden(AP, SymbolIterator, values);
    hidden(AP, "keys", function () {
      return arrayIterator(Object(this), function (i) { return i; });
    });
    hidden(AP, "entries", function () {
      var o = Object(this);
      return arrayIterator(o, function (i) { return [i, o[i]]; });
    });

    var nativeJoin = AP.join;
    var nativeSlice = AP.slice;

    // Elements can be long, so the total is only known by walking them.
    function joinStrings(parts, sep) {
      var total = 0;
      for (var i = 0; i < parts.length; i++) {
        total += parts[i].length + sep.length;
        if (total > MAX_STRING) tooLongText();
      }
      return apply(nativeJoin, parts, [sep]);
    }
    function join(o, sep, read) {
      checkLength(o, MAX_LENGTH);
      var parts = [];
      for (var i = 0; i < o.length; i++) {
        var v = o[i];
        parts[i] = v === undefined || v === null ? "" : read(v);
      }
      return joinStrings(parts, sep);
    }
    hidden(AP, "join", function (sep) {
      return join(Object(this), sep === undefined ? "," : toStr(sep), toStr);
    });
    hidden(AP, "toLocaleString", function () {
      return join(Object(this), ",", function (v) { return toStr(v.toLocaleString()); });
    });

    var nativeConcat = AP.concat;
    hidden(AP, "concat", function () {
      var total = 0;
      var all = [Object(this)];
      for (var i = 0; i < arguments.length; i++) all[i + 1] = arguments[i];
      for (var j = 0; j < all.length; j++) {
        var it = all[j];
        if (it !== null && typeof it === "object") {
          checkLength(it, MAX_LENGTH);
          total += +it.length || 0;
        } else {
          total++;
        }
        if (total > MAX_LENGTH) tooLong();
      }
      return apply(nativeConcat, this, arguments);
    });

    hidden(AP, "flat", function flat(depth) {
      var d = depth === undefined ? 1 : Number(depth);
      var out = [];
      (function walk(a, level) {
        checkLength(a, MAX_LENGTH);
        for (var i = 0; i < a.length; i++) {
          if (!(i in a)) continue;
          var v = a[i];
          if (level > 0 && isArray(v)) walk(v, level - 1);
          else out[out.length] = v;
        }
      })(this, d);
      return out;
    });

    var nativeFrom = Array.from;
    hidden(Array, "from", function (items, map, thisArg) {
      if (items !== null && items !== undefined && items[SymbolIterator] === undefined) checkLength(items, MAX_LENGTH);
      return apply(nativeFrom, this, arguments);
    });

    // ---- text ---------------------------------------------------------------------------------

    function text(v) {
      if (v === null || v === undefined) throw new TypeError("this method needs a string");
      // a String object through its own native, or String(v) would come back here through toString
      var s = typeof v === "string" ? v : v instanceof toStr ? apply(nativeValueOf, v, []) : toStr(v);
      if (s.length > MAX_STRING) tooLongText();
      return s;
    }

    var SP = String.prototype;
    var nativeValueOf = SP.valueOf;
    var special = {
      // element per character, or close to it
      split: function (orig) {
        return function (sep, limit) {
          var s = text(this);
          if (sep !== null && typeof sep === "object" && sep[Symbol.split]) return apply(orig, s, arguments);
          var most = limit === undefined ? MAX_LENGTH + 1 : Math.min(limit >>> 0, MAX_LENGTH + 1);
          var out = apply(orig, s, [sep, most]);
          if (out.length > MAX_LENGTH) tooLong();
          return out;
        };
      },
      repeat: function (orig) {
        return function (n) {
          var s = text(this);
          if (s.length * Number(n) > MAX_STRING) tooLongText();
          return apply(orig, s, [n]);
        };
      },
      padStart: function (orig) { return pad(orig); },
      padEnd: function (orig) { return pad(orig); },
      concat: function (orig) {
        return function () {
          var s = text(this);
          var total = s.length;
          var all = [];
          for (var i = 0; i < arguments.length; i++) {
            all[i] = text(arguments[i]);
            total += all[i].length;
            if (total > MAX_STRING) tooLongText();
          }
          return apply(orig, s, all);
        };
      },
      replace: function (orig) { return replacer(orig, false); },
      replaceAll: function (orig) { return replacer(orig, true); },
      // an element per match, through the regex's own method, which is checked below
      normalize: function (orig) {
        return function () {
          var s = text(this);
          if (s.length > MAX_LENGTH) tooLongText(); // it can grow eighteen times
          return apply(orig, s, arguments);
        };
      },
    };
    function pad(orig) {
      return function (n) {
        var s = text(this);
        if (Number(n) > MAX_STRING) tooLongText();
        return apply(orig, s, arguments);
      };
    }
    // What a replacement can make: its text at every match, and for $ patterns what they bring back.
    function replaceCost(s, value, everywhere) {
      var matches = everywhere ? s.length + 1 : 1;
      var cost = s.length + matches * value.length;
      if (value.indexOf("$") >= 0) {
        var tail = value.indexOf("$`") >= 0 || value.indexOf("$'") >= 0;
        cost += tail ? matches * s.length : s.length * (value.split("$").length - 1);
      }
      if (cost > MAX_STRING) tooLongText();
    }
    function replacer(orig, all) {
      return function (pattern, value) {
        var s = text(this);
        if (typeof value === "function") return apply(orig, s, [pattern, value]);
        var v = text(value);
        var everywhere = all || (pattern !== null && typeof pattern === "object");
        replaceCost(s, v, everywhere);
        return apply(orig, s, [pattern, v]);
      };
    }
    names(SP).forEach(function (name) {
      var orig = SP[name];
      // toString and valueOf only hand back what is there
      if (name === "constructor" || name === "length" || name === "toString" || name === "valueOf" || typeof orig !== "function") return;
      hidden(SP, name, special[name] ? special[name](orig) : function () {
        return apply(orig, text(this), arguments);
      });
    });
    hidden(SP, SymbolIterator, function () {
      var s = text(this);
      var at = 0;
      return iterator(function () {
        if (at >= s.length) return { value: undefined, done: true };
        var c = s.codePointAt(at);
        var v = c > 0xffff ? s.slice(at, at + 2) : s.charAt(at);
        at += v.length;
        return { value: v, done: false };
      });
    });

    var nativeRaw = String.raw;
    hidden(String, "raw", function (t) {
      if (t !== null && typeof t === "object") checkLength(t.raw, MAX_ARGS);
      return apply(nativeRaw, this, arguments);
    });
    ["encodeURI", "encodeURIComponent", "decodeURI", "decodeURIComponent", "escape", "unescape"].forEach(function (name) {
      var orig = globalThis[name];
      if (typeof orig !== "function") return;
      hidden(globalThis, name, function (v) { return orig(text(v)); });
    });

    // ---- regexes ------------------------------------------------------------------------------

    var NativeRegExp = RegExp;
    var RP = NativeRegExp.prototype;
    var SafeRegExp = function RegExp(source, flags) {
      var src = source instanceof NativeRegExp ? source.source : source;
      if (src !== undefined && text(src).length > MAX_REGEX_SOURCE) tooLongText();
      return arguments.length > 1 ? new NativeRegExp(source, flags) : new NativeRegExp(source);
    };
    define(SafeRegExp, "prototype", { value: RP });
    hidden(RP, "constructor", SafeRegExp);
    hidden(globalThis, "RegExp", SafeRegExp);
    delete RP.compile;
    function regexInput(v) {
      var s = text(v);
      if (s.length > MAX_REGEX_INPUT) tooLongText();
      return s;
    }
    [Symbol.match, Symbol.matchAll, Symbol.search, Symbol.split, "exec", "test"].forEach(function (name) {
      var orig = RP[name];
      if (typeof orig !== "function") return;
      hidden(RP, name, function (input, limit) {
        var s = regexInput(input);
        if (name === Symbol.split) {
          var most = limit === undefined ? MAX_LENGTH + 1 : Math.min(limit >>> 0, MAX_LENGTH + 1);
          var out = apply(orig, this, [s, most]);
          if (out.length > MAX_LENGTH) tooLong();
          return out;
        }
        return apply(orig, this, [s]);
      });
    });
    var nativeRegexReplace = RP[Symbol.replace];
    hidden(RP, Symbol.replace, function (input, value) {
      var s = regexInput(input);
      if (typeof value === "function") return apply(nativeRegexReplace, this, [s, value]);
      var v = text(value);
      replaceCost(s, v, true);
      return apply(nativeRegexReplace, this, [s, v]);
    });

    // ---- objects ------------------------------------------------------------------------------

    // A string has a property per character, and these make something per property.
    function guardArgs(owner, skip) {
      names(owner).forEach(function (name) {
        var orig = owner[name];
        if (typeof orig !== "function" || skip.indexOf(name) >= 0) return;
        hidden(owner, name, function () {
          for (var i = 0; i < arguments.length; i++) {
            var a = arguments[i];
            if (typeof a === "string") {
              if (a.length > MAX_LENGTH) tooLong();
            } else if (a instanceof String && a.length > MAX_LENGTH) tooLong();
          }
          return apply(orig, this, arguments);
        });
      });
    }
    guardArgs(Object, ["prototype"]);
    guardArgs(Reflect, ["apply", "construct"]);

    // ---- JSON ---------------------------------------------------------------------------------

    // the engine's JSON.stringify recurses in Go, and a getter can make a value as deep as it wants
    function stringify(value, replacer, space) {
      var indent = "";
      if (typeof space === "number") indent = "          ".slice(0, Math.max(0, Math.min(10, space)));
      else if (typeof space === "string") indent = space.slice(0, 10);
      var allow = isArray(replacer) ? replacer.map(toStr) : null;
      var fn = typeof replacer === "function" ? replacer : null;
      var stack = [];

      function ser(holder, key, cur) {
        var v = holder[key];
        if (v !== null && (typeof v === "object" || typeof v === "bigint") && typeof v.toJSON === "function") {
          v = v.toJSON(key);
        }
        if (fn) v = apply(fn, holder, [key, v]);
        if (v instanceof Number) v = Number(v);
        else if (v instanceof String) v = toStr(v);
        else if (v instanceof Boolean) v = v.valueOf();
        switch (typeof v) {
          case "string":
            if (v.length > MAX_STRING) tooLongText();
            return nativeStringify(v);
          case "number": return isFinite(v) ? toStr(v) : "null";
          case "boolean": return v ? "true" : "false";
          case "bigint": throw new TypeError("a BigInt cannot be written as JSON");
          case "object": break;
          default: return undefined;
        }
        if (v === null) return "null";
        for (var s = 0; s < stack.length; s++) {
          if (stack[s] === v) throw new TypeError("a circular value cannot be written as JSON");
        }
        stack[stack.length] = v;
        var inner = cur + indent, parts = [], text, i;
        if (isArray(v)) {
          for (i = 0; i < v.length; i++) {
            text = ser(v, toStr(i), inner);
            parts[parts.length] = text === undefined ? "null" : text;
          }
          text = parts.length === 0 ? "[]" : indent
            ? "[\n" + inner + joinStrings(parts, ",\n" + inner) + "\n" + cur + "]"
            : "[" + joinStrings(parts, ",") + "]";
        } else {
          var props = allow || keys(v);
          for (i = 0; i < props.length; i++) {
            text = ser(v, props[i], inner);
            if (text !== undefined) {
              parts[parts.length] = nativeStringify(props[i]) + (indent ? ": " : ":") + text;
            }
          }
          text = parts.length === 0 ? "{}" : indent
            ? "{\n" + inner + joinStrings(parts, ",\n" + inner) + "\n" + cur + "}"
            : "{" + joinStrings(parts, ",") + "}";
        }
        stack.length = stack.length - 1;
        return text;
      }
      return ser({ "": value }, "", "");
    }

    function parse(input, reviver) {
      var s = text(input);
      if (!jsonDepthOK(s)) throw new SyntaxError("this JSON is nested too deep");
      return nativeParse(s, reviver);
    }

    hidden(JSON, "stringify", stringify);
    hidden(JSON, "parse", parse);
    return { stringify: stringify, parse: parse };
  };
})();
