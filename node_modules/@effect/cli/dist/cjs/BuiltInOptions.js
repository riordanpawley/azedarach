"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.showWizard = exports.showVersion = exports.showHelp = exports.showCompletions = exports.isShowWizard = exports.isShowVersion = exports.isShowHelp = exports.isShowCompletions = exports.builtInOptions = void 0;
var InternalBuiltInOptions = _interopRequireWildcard(require("./internal/builtInOptions.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 */

/**
 * @since 1.0.0
 * @category options
 */
const builtInOptions = exports.builtInOptions = InternalBuiltInOptions.builtInOptions;
/**
 * @since 1.0.0
 * @category refinements
 */
const isShowCompletions = exports.isShowCompletions = InternalBuiltInOptions.isShowCompletions;
/**
 * @since 1.0.0
 * @category refinements
 */
const isShowHelp = exports.isShowHelp = InternalBuiltInOptions.isShowHelp;
/**
 * @since 1.0.0
 * @category refinements
 */
const isShowWizard = exports.isShowWizard = InternalBuiltInOptions.isShowWizard;
/**
 * @since 1.0.0
 * @category refinements
 */
const isShowVersion = exports.isShowVersion = InternalBuiltInOptions.isShowVersion;
/**
 * @since 1.0.0
 * @category constructors
 */
const showCompletions = exports.showCompletions = InternalBuiltInOptions.showCompletions;
/**
 * @since 1.0.0
 * @category constructors
 */
const showHelp = exports.showHelp = InternalBuiltInOptions.showHelp;
/**
 * @since 1.0.0
 * @category constructors
 */
const showWizard = exports.showWizard = InternalBuiltInOptions.showWizard;
/**
 * @since 1.0.0
 * @category constructors
 */
const showVersion = exports.showVersion = InternalBuiltInOptions.showVersion;
//# sourceMappingURL=BuiltInOptions.js.map