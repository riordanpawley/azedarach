"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.repeated = exports.optional = exports.named = exports.mixed = exports.getHelp = exports.enumerate = exports.empty = exports.concat = exports.alternation = void 0;
var InternalUsage = _interopRequireWildcard(require("./internal/usage.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 * @category combinators
 */
const alternation = exports.alternation = InternalUsage.alternation;
/**
 * @since 1.0.0
 * @category combinators
 */
const concat = exports.concat = InternalUsage.concat;
/**
 * @since 1.0.0
 * @category constructors
 */
const empty = exports.empty = InternalUsage.empty;
/**
 * @since 1.0.0
 * @category constructors
 */
const enumerate = exports.enumerate = InternalUsage.enumerate;
/**
 * @since 1.0.0
 * @category combinators
 */
const getHelp = exports.getHelp = InternalUsage.getHelp;
/**
 * @since 1.0.0
 * @category constructors
 */
const mixed = exports.mixed = InternalUsage.mixed;
/**
 * @since 1.0.0
 * @category constructors
 */
const named = exports.named = InternalUsage.named;
/**
 * @since 1.0.0
 * @category combinators
 */
const optional = exports.optional = InternalUsage.optional;
/**
 * @since 1.0.0
 * @category combinators
 */
const repeated = exports.repeated = InternalUsage.repeated;
//# sourceMappingURL=Usage.js.map