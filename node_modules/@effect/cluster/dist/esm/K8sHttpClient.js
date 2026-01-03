/**
 * @since 1.0.0
 */
import * as FileSystem from "@effect/platform/FileSystem";
import * as HttpClient from "@effect/platform/HttpClient";
import * as HttpClientRequest from "@effect/platform/HttpClientRequest";
import * as HttpClientResponse from "@effect/platform/HttpClientResponse";
import * as Context from "effect/Context";
import * as Effect from "effect/Effect";
import { identity } from "effect/Function";
import * as Layer from "effect/Layer";
import * as Option from "effect/Option";
import * as Schedule from "effect/Schedule";
import * as Schema from "effect/Schema";
/**
 * @since 1.0.0
 * @category Tags
 */
export class K8sHttpClient extends /*#__PURE__*/Context.Tag("@effect/cluster/K8sHttpClient")() {}
/**
 * @since 1.0.0
 * @category Layers
 */
export const layer = /*#__PURE__*/Layer.effect(K8sHttpClient, /*#__PURE__*/Effect.gen(function* () {
  const fs = yield* FileSystem.FileSystem;
  const token = yield* fs.readFileString("/var/run/secrets/kubernetes.io/serviceaccount/token").pipe(Effect.option);
  return (yield* HttpClient.HttpClient).pipe(HttpClient.mapRequest(HttpClientRequest.prependUrl("https://kubernetes.default.svc/api")), token._tag === "Some" ? HttpClient.mapRequest(HttpClientRequest.bearerToken(token.value.trim())) : identity, HttpClient.filterStatusOk, HttpClient.retryTransient({
    schedule: Schedule.spaced(5000)
  }));
}));
/**
 * @since 1.0.0
 * @category Constructors
 */
export const makeGetPods = /*#__PURE__*/Effect.fnUntraced(function* (options) {
  const client = yield* K8sHttpClient;
  const getPods = HttpClientRequest.get(options?.namespace ? `/v1/namespaces/${options.namespace}/pods` : "/v1/pods").pipe(HttpClientRequest.setUrlParam("fieldSelector", "status.phase=Running"), options?.labelSelector ? HttpClientRequest.setUrlParam("labelSelector", options.labelSelector) : identity);
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
export const makeCreatePod = /*#__PURE__*/Effect.gen(function* () {
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
export class PodStatus extends /*#__PURE__*/Schema.Class("@effect/cluster/K8sHttpClient/PodStatus")({
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
export class Pod extends /*#__PURE__*/Schema.Class("@effect/cluster/K8sHttpClient/Pod")({
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
const PodList = /*#__PURE__*/Schema.Struct({
  items: /*#__PURE__*/Schema.Array(Pod)
});
//# sourceMappingURL=K8sHttpClient.js.map