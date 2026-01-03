"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.layer = void 0;
var Layer = _interopRequireWildcard(require("effect/Layer"));
var MessageStorage = _interopRequireWildcard(require("./MessageStorage.js"));
var RunnerHealth = _interopRequireWildcard(require("./RunnerHealth.js"));
var Runners = _interopRequireWildcard(require("./Runners.js"));
var RunnerStorage = _interopRequireWildcard(require("./RunnerStorage.js"));
var Sharding = _interopRequireWildcard(require("./Sharding.js"));
var ShardingConfig = _interopRequireWildcard(require("./ShardingConfig.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 */

/**
 * An in-memory cluster that can be used for testing purposes.
 *
 * MessageStorage is backed by an in-memory driver, and RunnerStorage is backed
 * by an in-memory driver.
 *
 * @since 1.0.0
 * @category Layers
 */
const layer = exports.layer = /*#__PURE__*/Sharding.layer.pipe(/*#__PURE__*/Layer.provideMerge(Runners.layerNoop), /*#__PURE__*/Layer.provideMerge(MessageStorage.layerMemory), /*#__PURE__*/Layer.provide([RunnerStorage.layerMemory, RunnerHealth.layerNoop]), /*#__PURE__*/Layer.provide(/*#__PURE__*/ShardingConfig.layer()));
//# sourceMappingURL=TestRunner.js.map