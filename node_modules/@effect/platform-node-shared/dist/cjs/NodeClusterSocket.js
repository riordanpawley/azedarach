"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.layerSocketServer = exports.layerClientProtocol = void 0;
var Runners = _interopRequireWildcard(require("@effect/cluster/Runners"));
var ShardingConfig = _interopRequireWildcard(require("@effect/cluster/ShardingConfig"));
var _Socket = require("@effect/platform/Socket");
var RpcClient = _interopRequireWildcard(require("@effect/rpc/RpcClient"));
var RpcSerialization = _interopRequireWildcard(require("@effect/rpc/RpcSerialization"));
var Effect = _interopRequireWildcard(require("effect/Effect"));
var Layer = _interopRequireWildcard(require("effect/Layer"));
var Option = _interopRequireWildcard(require("effect/Option"));
var NodeSocket = _interopRequireWildcard(require("./NodeSocket.js"));
var NodeSocketServer = _interopRequireWildcard(require("./NodeSocketServer.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 */

/**
 * @since 1.0.0
 * @category Layers
 */
const layerClientProtocol = exports.layerClientProtocol = /*#__PURE__*/Layer.effect(Runners.RpcClientProtocol)(/*#__PURE__*/Effect.gen(function* () {
  const serialization = yield* RpcSerialization.RpcSerialization;
  return Effect.fnUntraced(function* (address) {
    const socket = yield* NodeSocket.makeNet({
      openTimeout: 1000,
      timeout: 5500,
      host: address.host,
      port: address.port
    });
    return yield* RpcClient.makeProtocolSocket().pipe(Effect.provideService(_Socket.Socket, socket), Effect.provideService(RpcSerialization.RpcSerialization, serialization));
  }, Effect.orDie);
}));
/**
 * @since 1.0.0
 * @category Layers
 */
const layerSocketServer = exports.layerSocketServer = /*#__PURE__*/Effect.gen(function* () {
  const config = yield* ShardingConfig.ShardingConfig;
  const listenAddress = Option.orElse(config.runnerListenAddress, () => config.runnerAddress);
  if (listenAddress._tag === "None") {
    return yield* Effect.die("layerSocketServer: ShardingConfig.runnerListenAddress is None");
  }
  return NodeSocketServer.layer(listenAddress.value);
}).pipe(Layer.unwrapEffect);
//# sourceMappingURL=NodeClusterSocket.js.map