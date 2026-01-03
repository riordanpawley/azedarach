"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.makeMemory = exports.makeEncoded = exports.layerMemory = exports.RunnerStorage = void 0;
var _Array = require("effect/Array");
var Context = _interopRequireWildcard(require("effect/Context"));
var Effect = _interopRequireWildcard(require("effect/Effect"));
var Layer = _interopRequireWildcard(require("effect/Layer"));
var MutableHashMap = _interopRequireWildcard(require("effect/MutableHashMap"));
var MachineId = _interopRequireWildcard(require("./MachineId.js"));
var _Runner = require("./Runner.js");
var _ShardId = require("./ShardId.js");
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 */

/**
 * Represents a generic interface to the persistent storage required by the
 * cluster.
 *
 * @since 1.0.0
 * @category models
 */
class RunnerStorage extends /*#__PURE__*/Context.Tag("@effect/cluster/RunnerStorage")() {}
/**
 * @since 1.0.0
 * @category layers
 */
exports.RunnerStorage = RunnerStorage;
const makeEncoded = encoded => RunnerStorage.of({
  getRunners: Effect.gen(function* () {
    const runners = yield* encoded.getRunners;
    const results = [];
    for (let i = 0; i < runners.length; i++) {
      const [runner, healthy] = runners[i];
      try {
        results.push([_Runner.Runner.decodeSync(runner), healthy]);
      } catch {
        //
      }
    }
    return results;
  }),
  register: (runner, healthy) => Effect.map(encoded.register(encodeRunnerAddress(runner.address), _Runner.Runner.encodeSync(runner), healthy), MachineId.make),
  unregister: address => encoded.unregister(encodeRunnerAddress(address)),
  setRunnerHealth: (address, healthy) => encoded.setRunnerHealth(encodeRunnerAddress(address), healthy),
  acquire: (address, shardIds) => {
    const arr = Array.from(shardIds, id => id.toString());
    if (!(0, _Array.isNonEmptyArray)(arr)) return Effect.succeed([]);
    return encoded.acquire(encodeRunnerAddress(address), arr).pipe(Effect.map(shards => shards.map(_ShardId.ShardId.fromString)));
  },
  refresh: (address, shardIds) => encoded.refresh(encodeRunnerAddress(address), Array.from(shardIds, id => id.toString())).pipe(Effect.map(shards => shards.map(_ShardId.ShardId.fromString))),
  release(address, shardId) {
    return encoded.release(encodeRunnerAddress(address), shardId.toString());
  },
  releaseAll(address) {
    return encoded.releaseAll(encodeRunnerAddress(address));
  }
});
/**
 * @since 1.0.0
 * @category constructors
 */
exports.makeEncoded = makeEncoded;
const makeMemory = exports.makeMemory = /*#__PURE__*/Effect.gen(function* () {
  const runners = MutableHashMap.empty();
  let acquired = [];
  let id = 0;
  return RunnerStorage.of({
    getRunners: Effect.sync(() => Array.from(MutableHashMap.values(runners), runner => [runner, true])),
    register: runner => Effect.sync(() => {
      MutableHashMap.set(runners, runner.address, runner);
      return MachineId.make(id++);
    }),
    unregister: address => Effect.sync(() => {
      MutableHashMap.remove(runners, address);
    }),
    setRunnerHealth: () => Effect.void,
    acquire: (_address, shardIds) => {
      acquired = Array.from(shardIds);
      return Effect.succeed(Array.from(shardIds));
    },
    refresh: () => Effect.sync(() => acquired),
    release: () => Effect.void,
    releaseAll: () => Effect.void
  });
});
/**
 * @since 1.0.0
 * @category layers
 */
const layerMemory = exports.layerMemory = /*#__PURE__*/Layer.effect(RunnerStorage)(makeMemory);
// -------------------------------------------------------------------------------------
// internal
// -------------------------------------------------------------------------------------
const encodeRunnerAddress = runnerAddress => `${runnerAddress.host}:${runnerAddress.port}`;
//# sourceMappingURL=RunnerStorage.js.map