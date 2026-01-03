"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.wizard = exports.withSubcommands = exports.withDescription = exports.prompt = exports.parse = exports.mapEffect = exports.map = exports.make = exports.getZshCompletions = exports.getUsage = exports.getSubcommands = exports.getNames = exports.getHelp = exports.getFishCompletions = exports.getBashCompletions = exports.TypeId = void 0;
var Internal = _interopRequireWildcard(require("./internal/commandDescriptor.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 * @category symbols
 */
const TypeId = exports.TypeId = Internal.TypeId;
/**
 * @since 1.0.0
 * @category combinators
 */
const getHelp = exports.getHelp = Internal.getHelp;
/**
 * @since 1.0.0
 * @category combinators
 */
const getBashCompletions = exports.getBashCompletions = Internal.getBashCompletions;
/**
 * @since 1.0.0
 * @category combinators
 */
const getFishCompletions = exports.getFishCompletions = Internal.getFishCompletions;
/**
 * @since 1.0.0
 * @category combinators
 */
const getZshCompletions = exports.getZshCompletions = Internal.getZshCompletions;
/**
 * @since 1.0.0
 * @category combinators
 */
const getNames = exports.getNames = Internal.getNames;
/**
 * @since 1.0.0
 * @category combinators
 */
const getSubcommands = exports.getSubcommands = Internal.getSubcommands;
/**
 * @since 1.0.0
 * @category combinators
 */
const getUsage = exports.getUsage = Internal.getUsage;
/**
 * @since 1.0.0
 * @category combinators
 */
const map = exports.map = Internal.map;
/**
 * @since 1.0.0
 * @category combinators
 */
const mapEffect = exports.mapEffect = Internal.mapEffect;
/**
 * @since 1.0.0
 * @category combinators
 */
const parse = exports.parse = Internal.parse;
/**
 * @since 1.0.0
 * @category constructors
 */
const prompt = exports.prompt = Internal.prompt;
/**
 * @since 1.0.0
 * @category constructors
 */
const make = exports.make = Internal.make;
/**
 * @since 1.0.0
 * @category combinators
 */
const withDescription = exports.withDescription = Internal.withDescription;
/**
 * @since 1.0.0
 * @category combinators
 */
const withSubcommands = exports.withSubcommands = Internal.withSubcommands;
/**
 * @since 1.0.0
 * @category combinators
 */
const wizard = exports.wizard = Internal.wizard;
//# sourceMappingURL=CommandDescriptor.js.map