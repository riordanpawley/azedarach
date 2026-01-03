/**
 * @since 1.0.0
 */
import * as Context from "effect/Context";
import type { EntityId } from "./EntityId.js";
declare const Persisted_base: Context.ReferenceClass<Persisted, "@effect/cluster/ClusterSchema/Persisted", boolean>;
/**
 * @since 1.0.0
 * @category Annotations
 */
export declare class Persisted extends Persisted_base {
}
declare const Uninterruptible_base: Context.ReferenceClass<Uninterruptible, "@effect/cluster/ClusterSchema/Uninterruptible", boolean | "client" | "server">;
/**
 * @since 1.0.0
 * @category Annotations
 */
export declare class Uninterruptible extends Uninterruptible_base {
    /**
     * @since 1.0.0
     */
    static forServer(context: Context.Context<never>): boolean;
    /**
     * @since 1.0.0
     */
    static forClient(context: Context.Context<never>): boolean;
}
declare const ShardGroup_base: Context.ReferenceClass<ShardGroup, "@effect/cluster/ClusterSchema/ShardGroup", (entityId: EntityId) => string>;
/**
 * @since 1.0.0
 * @category Annotations
 */
export declare class ShardGroup extends ShardGroup_base {
}
declare const ClientTracingEnabled_base: Context.ReferenceClass<ClientTracingEnabled, "@effect/cluster/ClusterSchema/ClientTracingEnabled", boolean>;
/**
 * @since 1.0.0
 * @category Annotations
 */
export declare class ClientTracingEnabled extends ClientTracingEnabled_base {
}
export {};
//# sourceMappingURL=ClusterSchema.d.ts.map