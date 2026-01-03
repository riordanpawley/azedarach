/**
 * @since 1.0.0
 */
import * as PersistedQueue from "@effect/experimental/PersistedQueue";
import * as Duration from "effect/Duration";
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import type * as Scope from "effect/Scope";
import * as SqlClient from "./SqlClient.js";
import type { SqlError } from "./SqlError.js";
/**
 * @since 1.0.0
 * @category constructors
 */
export declare const make: (options?: {
    readonly tableName?: string | undefined;
    readonly pollInterval?: Duration.DurationInput | undefined;
    readonly lockRefreshInterval?: Duration.DurationInput | undefined;
    readonly lockExpiration?: Duration.DurationInput | undefined;
} | undefined) => Effect.Effect<PersistedQueue.PersistedQueueStore["Type"], SqlError, SqlClient.SqlClient | Scope.Scope>;
/**
 * @since 1.0.0
 * @category layers
 */
export declare const layerStore: (options?: {
    readonly tableName?: string | undefined;
    readonly pollInterval?: Duration.DurationInput | undefined;
    readonly lockRefreshInterval?: Duration.DurationInput | undefined;
    readonly lockExpiration?: Duration.DurationInput | undefined;
}) => Layer.Layer<PersistedQueue.PersistedQueueStore, SqlError, SqlClient.SqlClient>;
//# sourceMappingURL=SqlPersistedQueue.d.ts.map