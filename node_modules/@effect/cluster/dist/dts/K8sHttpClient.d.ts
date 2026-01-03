/**
 * @since 1.0.0
 */
import * as FileSystem from "@effect/platform/FileSystem";
import * as HttpClient from "@effect/platform/HttpClient";
import type * as HttpClientError from "@effect/platform/HttpClientError";
import * as Context from "effect/Context";
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import type * as ParseResult from "effect/ParseResult";
import * as Schema from "effect/Schema";
import type * as v1 from "kubernetes-types/core/v1.d.ts";
declare const K8sHttpClient_base: Context.TagClass<K8sHttpClient, "@effect/cluster/K8sHttpClient", HttpClient.HttpClient>;
/**
 * @since 1.0.0
 * @category Tags
 */
export declare class K8sHttpClient extends K8sHttpClient_base {
}
/**
 * @since 1.0.0
 * @category Layers
 */
export declare const layer: Layer.Layer<K8sHttpClient, never, HttpClient.HttpClient | FileSystem.FileSystem>;
/**
 * @since 1.0.0
 * @category Constructors
 */
export declare const makeGetPods: (options?: {
    readonly namespace?: string | undefined;
    readonly labelSelector?: string | undefined;
} | undefined) => Effect.Effect<Effect.Effect<Map<string, Pod>, HttpClientError.HttpClientError | ParseResult.ParseError, never>, never, K8sHttpClient>;
/**
 * @since 1.0.0
 * @category Constructors
 */
export declare const makeCreatePod: Effect.Effect<(spec: v1.Pod) => Effect.Effect<PodStatus, never, import("effect/Scope").Scope>, never, K8sHttpClient>;
declare const PodStatus_base: Schema.Class<PodStatus, {
    phase: typeof Schema.String;
    conditions: Schema.Array$<Schema.Struct<{
        type: typeof Schema.String;
        status: typeof Schema.String;
        lastTransitionTime: typeof Schema.String;
    }>>;
    podIP: typeof Schema.String;
    hostIP: typeof Schema.String;
}, Schema.Struct.Encoded<{
    phase: typeof Schema.String;
    conditions: Schema.Array$<Schema.Struct<{
        type: typeof Schema.String;
        status: typeof Schema.String;
        lastTransitionTime: typeof Schema.String;
    }>>;
    podIP: typeof Schema.String;
    hostIP: typeof Schema.String;
}>, never, {
    readonly phase: string;
} & {
    readonly podIP: string;
} & {
    readonly hostIP: string;
} & {
    readonly conditions: readonly {
        readonly type: string;
        readonly status: string;
        readonly lastTransitionTime: string;
    }[];
}, {}, {}>;
/**
 * @since 1.0.0
 * @category Schemas
 */
export declare class PodStatus extends PodStatus_base {
}
declare const Pod_base: Schema.Class<Pod, {
    status: typeof PodStatus;
}, Schema.Struct.Encoded<{
    status: typeof PodStatus;
}>, never, {
    readonly status: PodStatus;
}, {}, {}>;
/**
 * @since 1.0.0
 * @category Schemas
 */
export declare class Pod extends Pod_base {
    get isReady(): boolean;
    get isReadyOrInitializing(): boolean;
}
export {};
//# sourceMappingURL=K8sHttpClient.d.ts.map