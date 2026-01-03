"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.normalizeCase = exports.make = exports.layer = exports.defaultLayer = exports.defaultConfig = exports.Tag = void 0;
var Context = _interopRequireWildcard(require("effect/Context"));
var _Function = require("effect/Function");
var Layer = _interopRequireWildcard(require("effect/Layer"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/** @internal */
const make = params => ({
  ...defaultConfig,
  ...params
});
/** @internal */
exports.make = make;
const Tag = exports.Tag = /*#__PURE__*/Context.GenericTag("@effect/cli/CliConfig");
/** @internal */
const defaultConfig = exports.defaultConfig = {
  isCaseSensitive: false,
  autoCorrectLimit: 2,
  finalCheckBuiltIn: false,
  showAllNames: true,
  showBuiltIns: true,
  showTypes: true
};
/** @internal */
const defaultLayer = exports.defaultLayer = /*#__PURE__*/Layer.succeed(Tag, defaultConfig);
/** @internal */
const layer = config => Layer.succeed(Tag, make(config));
/** @internal */
exports.layer = layer;
const normalizeCase = exports.normalizeCase = /*#__PURE__*/(0, _Function.dual)(2, (self, text) => self.isCaseSensitive ? text : text.toLowerCase());
//# sourceMappingURL=cliConfig.js.map