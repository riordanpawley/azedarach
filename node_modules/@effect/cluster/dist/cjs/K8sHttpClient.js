"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.makeGetPods = exports.makeCreatePod = exports.layer = exports.PodStatus = exports.Pod = exports.K8sHttpClient = void 0;
var FileSystem = _interopRequireWildcard(require("@effect/platform/FileSystem"));
var HttpClient = _interopRequireWildcard(require("@effect/platform/HttpClient"));
var HttpClientRequest = _interopRequireWildcard(require("@effect/platform/HttpClientRequest"));
var HttpClientResponse = _interopRequireWildcard(require("@effect/platform/HttpClientResponse"));
var Context = _interopRequireWildcard(require("effect/Context"));
var Effect = _interopRequireWildcard(require("effect/Effect"));
var _Function = require("effect/Function");
var Layer = _interopRequireWildcard(require("effect/Layer"));
var Option = _interopRequireWildcard(require("effect/Option"));
var Schedule = _interopRequireWildcard(require("effect/Schedule"));
var Schema = _interopRequireWildcard(require("effect/Schema"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 */

/**
 * @since 1.0.0
 * @category Tags
 */
class K8sHttpClient extends /*#__PURE__*/Context.Tag("@effect/cluster/K8sHttpClient")() {}
/**
 * @since 1.0.0
 * @category Layers
 */
exports.K8sHttpClient = K8sHttpClient;
const layer = exports.layer = /*#__PURE__*/Layer.effect(K8sHttpClient, /*#__PURE__*/Effect.gen(function* () {
  const fs = yield* FileSystem.FileSystem;
  const token = yield* fs.readFileString("/var/run/secrets/kubernetes.io/serviceaccount/token").pipe(Effect.option);
  return (yield* HttpClient.HttpClient).pipe(HttpClient.mapRequest(HttpClientRequest.prependUrl("https://kubernetes.default.svc/api")), token._tag === "Some" ? HttpClient.mapRequest(HttpClientRequest.bearerToken(token.value.trim())) : _Function.identity, HttpClient.filterStatusOk, HttpClient.retryTransient({
    schedule: Schedule.spaced(5000)
  }));
}));
/**
 * @since 1.0.0
 * @category Constructors
 */
const makeGetPods = exports.makeGetPods = /*#__PURE__*/Effect.fnUntraced(function* (options) {
  const client = yield* K8sHttpClient;
  const getPods = HttpClientRequest.get(options?.namespace ? `/v1/namespaces/${options.namespace}/pods` : "/v1/pods").pipe(HttpClientRequest.setUrlParam("fieldSelector", "status.phase=Running"), options?.labelSelector ? HttpClientRequest.setUrlParam("labelSelector", options.labelSelector) : _Function.identity);
  return yield* client.execute(getPods).pipe(Effect.flatMap(HttpClientResponse.schemaBodyJson(PodList)), Effect.map(list => {
    const pods = new Map();
    for (let i = 0; i < list.items.length; i++) {
      const pod = list.items[i];
      pods.set(pod.status.podIP, pod);
    }
    return pods;
  }), Effect.tapErrorCause(cause => Effect.logWarning("Failed to fetch pods from Kubernetes API", cause)), Effect.cachedWithTTL("10 seconds"));
});
/**
 * @since 1.0.0
 * @category Constructors
 */
const makeCreatePod = exports.makeCreatePod = /*#__PURE__*/Effect.gen(function* () {
  const client = yield* K8sHttpClient;
  return Effect.fnUntraced(function* (spec) {
    spec = {
      apiVersion: "v1",
      kind: "Pod",
      metadata: {
        namespace: "default",
        ...spec.metadata
      },
      ...spec
    };
    const namespace = spec.metadata?.namespace ?? "default";
    const name = spec.metadata.name;
    const readPodRaw = HttpClientRequest.get(`/v1/namespaces/${namespace}/pods/${name}`).pipe(client.execute);
    const readPod = readPodRaw.pipe(Effect.flatMap(HttpClientResponse.schemaBodyJson(Pod)), Effect.asSome, Effect.retry({
      while: e => e._tag === "ParseError",
      schedule: Schedule.spaced("1 seconds")
    }), Effect.catchIf(err => err._tag === "ResponseError" && err.response.status === 404, () => Effect.succeedNone), Effect.orDie);
    const isPodFound = readPodRaw.pipe(Effect.as(true), Effect.catchIf(err => err._tag === "ResponseError" && err.response.status === 404, () => Effect.succeed(false)));
    const createPod = HttpClientRequest.post(`/v1/namespaces/${namespace}/pods`).pipe(HttpClientRequest.bodyUnsafeJson(spec), client.execute, Effect.catchIf(err => err._tag === "ResponseError" && err.response.status === 409, () => readPod), Effect.tapErrorCause(Effect.logInfo), Effect.orDie);
    const deletePod = HttpClientRequest.del(`/v1/namespaces/${namespace}/pods/${name}`).pipe(client.execute, Effect.flatMap(res => res.json), Effect.catchIf(err => err._tag === "ResponseError" && err.response.status === 404, () => Effect.void), Effect.tapErrorCause(Effect.logInfo), Effect.orDie, Effect.asVoid);
    yield* Effect.addFinalizer(Effect.fnUntraced(function* () {
      yield* deletePod;
      yield* isPodFound.pipe(Effect.repeat({
        until: found => !found,
        schedule: Schedule.spaced("3 seconds")
      }), Effect.orDie);
    }));
    let opod = Option.none();
    while (Option.isNone(opod) || !opod.value.isReady) {
      if (Option.isNone(opod)) {
        yield* createPod;
      }
      yield* Effect.sleep("3 seconds");
      opod = yield* readPod;
    }
    return opod.value.status;
  }, Effect.withSpan("K8sHttpClient.createPod"));
});
/**
 * @since 1.0.0
 * @category Schemas
 */
class PodStatus extends /*#__PURE__*/Schema.Class("@effect/cluster/K8sHttpClient/PodStatus")({
  phase: Schema.String,
  conditions: /*#__PURE__*/Schema.Array(/*#__PURE__*/Schema.Struct({
    type: Schema.String,
    status: Schema.String,
    lastTransitionTime: Schema.String
  })),
  podIP: Schema.String,
  hostIP: Schema.String
}) {}
/**
 * @since 1.0.0
 * @category Schemas
 */
exports.PodStatus = PodStatus;
class Pod extends /*#__PURE__*/Schema.Class("@effect/cluster/K8sHttpClient/Pod")({
  status: PodStatus
}) {
  get isReady() {
    for (let i = 0; i < this.status.conditions.length; i++) {
      const condition = this.status.conditions[i];
      if (condition.type === "Ready") {
        return condition.status === "True";
      }
    }
    return false;
  }
  get isReadyOrInitializing() {
    let initializedAt;
    let readyAt;
    for (let i = 0; i < this.status.conditions.length; i++) {
      const condition = this.status.conditions[i];
      switch (condition.type) {
        case "Initialized":
          {
            if (condition.status !== "True") {
              return true;
            }
            initializedAt = condition.lastTransitionTime;
            break;
          }
        case "Ready":
          {
            if (condition.status === "True") {
              return true;
            }
            readyAt = condition.lastTransitionTime;
            break;
          }
      }
    }
    // if the pod is still booting up, consider it ready as it would have
    // already registered itself with RunnerStorage by now
    return initializedAt === readyAt;
  }
}
exports.Pod = Pod;
const PodList = /*#__PURE__*/Schema.Struct({
  items: /*#__PURE__*/Schema.Array(Pod)
});
//# sourceMappingURL=K8sHttpClient.js.map