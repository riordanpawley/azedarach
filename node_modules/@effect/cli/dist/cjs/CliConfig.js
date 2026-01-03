"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.normalizeCase = exports.make = exports.layer = exports.defaultLayer = exports.defaultConfig = exports.CliConfig = void 0;
var InternalCliConfig = _interopRequireWildcard(require("./internal/cliConfig.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 * @category context
 */
const CliConfig = exports.CliConfig = InternalCliConfig.Tag;
/**
 * @since 1.0.0
 * @category constructors
 */
const defaultConfig = exports.defaultConfig = InternalCliConfig.defaultConfig;
/**
 * @since 1.0.0
 * @category context
 */
const defaultLayer = exports.defaultLayer = InternalCliConfig.defaultLayer;
/**
 * @since 1.0.0
 * @category context
 */
const layer = exports.layer = InternalCliConfig.layer;
/**
 * @since 1.0.0
 * @category constructors
 */
const make = exports.make = InternalCliConfig.make;
/**
 * @since 1.0.0
 * @category utilities
 */
const normalizeCase = exports.normalizeCase = InternalCliConfig.normalizeCase;
//# sourceMappingURL=CliConfig.js.map