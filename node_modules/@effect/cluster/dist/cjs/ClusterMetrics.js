"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.singletons = exports.shards = exports.runnersHealthy = exports.runners = exports.entities = void 0;
var Metric = _interopRequireWildcard(require("effect/Metric"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 */

/**
 * @since 1.0.0
 * @category metrics
 */
const entities = exports.entities = /*#__PURE__*/Metric.gauge("effect_cluster_entities", {
  bigint: true
});
/**
 * @since 1.0.0
 * @category metrics
 */
const singletons = exports.singletons = /*#__PURE__*/Metric.gauge("effect_cluster_singletons", {
  bigint: true
});
/**
 * @since 1.0.0
 * @category metrics
 */
const runners = exports.runners = /*#__PURE__*/Metric.gauge("effect_cluster_runners", {
  bigint: true
});
/**
 * @since 1.0.0
 * @category metrics
 */
const runnersHealthy = exports.runnersHealthy = /*#__PURE__*/Metric.gauge("effect_cluster_runners_healthy", {
  bigint: true
});
/**
 * @since 1.0.0
 * @category metrics
 */
const shards = exports.shards = /*#__PURE__*/Metric.gauge("effect_cluster_shards", {
  bigint: true
});
//# sourceMappingURL=ClusterMetrics.js.map