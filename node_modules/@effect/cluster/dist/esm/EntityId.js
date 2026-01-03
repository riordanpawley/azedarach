/**
 * @since 1.0.0
 */
import * as Schema from "effect/Schema";
/**
 * @since 1.0.0
 * @category constructors
 */
export const EntityId = /*#__PURE__*/Schema.NonEmptyTrimmedString.pipe(/*#__PURE__*/Schema.brand("EntityId"));
/**
 * @since 1.0.0
 * @category constructors
 */
export const make = id => id;
//# sourceMappingURL=EntityId.js.map