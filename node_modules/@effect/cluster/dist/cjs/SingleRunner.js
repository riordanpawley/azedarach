"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.layer = void 0;
var Layer = _interopRequireWildcard(require("effect/Layer"));
var RunnerHealth = _interopRequireWildcard(require("./RunnerHealth.js"));
var Runners = _interopRequireWildcard(require("./Runners.js"));
var RunnerStorage = _interopRequireWildcard(require("./RunnerStorage.js"));
var Sharding = _interopRequireWildcard(require("./Sharding.js"));
var ShardingConfig = _interopRequireWildcard(require("./ShardingConfig.js"));
var SqlMessageStorage = _interopRequireWildcard(require("./SqlMessageStorage.js"));
var SqlRunnerStorage = _interopRequireWildcard(require("./SqlRunnerStorage.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * A sql backed single-node cluster, that can be used for running durable
 * entities and workflows.
 *
 * @since 1.0.0
 * @category Layers
 */
const layer = options => Sharding.layer.pipe(Layer.provideMerge(Runners.layerNoop), Layer.provideMerge(SqlMessageStorage.layer), Layer.provide([options?.runnerStorage === "memory" ? RunnerStorage.layerMemory : Layer.orDie(SqlRunnerStorage.layer), RunnerHealth.layerNoop]), Layer.provide(ShardingConfig.layerFromEnv(options?.shardingConfig)));
exports.layer = layer;
//# sourceMappingURL=SingleRunner.js.map