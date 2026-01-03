"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.wizard = exports.withSubcommands = exports.withHandler = exports.withDescription = exports.transformHandler = exports.run = exports.provideSync = exports.provideEffectDiscard = exports.provideEffect = exports.provide = exports.prompt = exports.make = exports.getZshCompletions = exports.getUsage = exports.getSubcommands = exports.getNames = exports.getHelp = exports.getFishCompletions = exports.getBashCompletions = exports.fromDescriptor = exports.TypeId = void 0;
var Internal = _interopRequireWildcard(require("./internal/command.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 * @category type ids
 */
const TypeId = exports.TypeId = Internal.TypeId;
/**
 * @since 1.0.0
 * @category constructors
 */
const fromDescriptor = exports.fromDescriptor = Internal.fromDescriptor;
/**
 * @since 1.0.0
 * @category accessors
 */
const getHelp = exports.getHelp = Internal.getHelp;
/**
 * @since 1.0.0
 * @category accessors
 */
const getNames = exports.getNames = Internal.getNames;
/**
 * @since 1.0.0
 * @category accessors
 */
const getBashCompletions = exports.getBashCompletions = Internal.getBashCompletions;
/**
 * @since 1.0.0
 * @category accessors
 */
const getFishCompletions = exports.getFishCompletions = Internal.getFishCompletions;
/**
 * @since 1.0.0
 * @category accessors
 */
const getZshCompletions = exports.getZshCompletions = Internal.getZshCompletions;
/**
 * @since 1.0.0
 * @category accessors
 */
const getSubcommands = exports.getSubcommands = Internal.getSubcommands;
/**
 * @since 1.0.0
 * @category accessors
 */
const getUsage = exports.getUsage = Internal.getUsage;
/**
 * @since 1.0.0
 * @category constructors
 */
const make = exports.make = Internal.make;
/**
 * @since 1.0.0
 * @category constructors
 */
const prompt = exports.prompt = Internal.prompt;
/**
 * @since 1.0.0
 * @category combinators
 */
const provide = exports.provide = Internal.provide;
/**
 * @since 1.0.0
 * @category combinators
 */
const provideEffect = exports.provideEffect = Internal.provideEffect;
/**
 * @since 1.0.0
 * @category combinators
 */
const provideEffectDiscard = exports.provideEffectDiscard = Internal.provideEffectDiscard;
/**
 * @since 1.0.0
 * @category combinators
 */
const provideSync = exports.provideSync = Internal.provideSync;
/**
 * @since 1.0.0
 * @category combinators
 */
const transformHandler = exports.transformHandler = Internal.transformHandler;
/**
 * @since 1.0.0
 * @category combinators
 */
const withDescription = exports.withDescription = Internal.withDescription;
/**
 * @since 1.0.0
 * @category combinators
 */
const withHandler = exports.withHandler = Internal.withHandler;
/**
 * @since 1.0.0
 * @category combinators
 */
const withSubcommands = exports.withSubcommands = Internal.withSubcommands;
/**
 * @since 1.0.0
 * @category accessors
 */
const wizard = exports.wizard = Internal.wizard;
/**
 * @since 1.0.0
 * @category conversions
 */
const run = exports.run = Internal.run;
//# sourceMappingURL=Command.js.map