"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.weak = exports.uri = exports.text = exports.strong = exports.spans = exports.space = exports.isWeak = exports.isUri = exports.isText = exports.isStrong = exports.isSequence = exports.error = exports.empty = exports.concat = exports.code = void 0;
var InternalSpan = _interopRequireWildcard(require("../internal/helpDoc/span.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 * @category refinements
 */
const isSequence = exports.isSequence = InternalSpan.isSequence;
/**
 * @since 1.0.0
 * @category refinements
 */
const isStrong = exports.isStrong = InternalSpan.isStrong;
/**
 * @since 1.0.0
 * @category refinements
 */
const isText = exports.isText = InternalSpan.isText;
/**
 * @since 1.0.0
 * @category refinements
 */
const isUri = exports.isUri = InternalSpan.isUri;
/**
 * @since 1.0.0
 * @category refinements
 */
const isWeak = exports.isWeak = InternalSpan.isWeak;
/**
 * @since 1.0.0
 * @category constructors
 */
const empty = exports.empty = InternalSpan.empty;
/**
 * @since 1.0.0
 * @category constructors
 */
const space = exports.space = InternalSpan.space;
/**
 * @since 1.0.0
 * @category constructors
 */
const text = exports.text = InternalSpan.text;
/**
 * @since 1.0.0
 * @category constructors
 */
const code = exports.code = InternalSpan.code;
/**
 * @since 1.0.0
 * @category constructors
 */
const error = exports.error = InternalSpan.error;
/**
 * @since 1.0.0
 * @category constructors
 */
const weak = exports.weak = InternalSpan.weak;
/**
 * @since 1.0.0
 * @category constructors
 */
const strong = exports.strong = InternalSpan.strong;
/**
 * @since 1.0.0
 * @category constructors
 */
const uri = exports.uri = InternalSpan.uri;
/**
 * @since 1.0.0
 * @category combinators
 */
const concat = exports.concat = InternalSpan.concat;
/**
 * @since 1.0.0
 * @category combinators
 */
const spans = exports.spans = InternalSpan.spans;
//# sourceMappingURL=Span.js.map