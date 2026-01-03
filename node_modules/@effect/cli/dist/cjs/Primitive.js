"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.wizard = exports.validate = exports.text = exports.isBool = exports.integer = exports.getTypeName = exports.getHelp = exports.getChoices = exports.float = exports.date = exports.choice = exports.boolean = exports.PrimitiveTypeId = void 0;
var InternalPrimitive = _interopRequireWildcard(require("./internal/primitive.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 * @category symbol
 */
const PrimitiveTypeId = exports.PrimitiveTypeId = InternalPrimitive.PrimitiveTypeId;
/**
 * @since 1.0.0
 * @category Predicates
 */
const isBool = exports.isBool = InternalPrimitive.isBool;
/**
 * Represents a boolean value.
 *
 * True values can be passed as one of: `["true", "1", "y", "yes" or "on"]`.
 * False value can be passed as one of: `["false", "o", "n", "no" or "off"]`.
 *
 * @since 1.0.0
 * @category constructors
 */
const boolean = exports.boolean = InternalPrimitive.boolean;
/**
 * @since 1.0.0
 * @category constructors
 */
const choice = exports.choice = InternalPrimitive.choice;
/**
 * Represents a date in ISO-8601 format, such as `2007-12-03T10:15:30`.
 *
 * @since 1.0.0
 * @category constructors
 */
const date = exports.date = InternalPrimitive.date;
/**
 * Represents a floating point number.
 *
 * @since 1.0.0
 * @category constructors
 */
const float = exports.float = InternalPrimitive.float;
/**
 * Returns a text representation of the valid choices for a primitive type, if
 * any.
 *
 * @since 1.0.0
 * @category combinators
 */
const getChoices = exports.getChoices = InternalPrimitive.getChoices;
/**
 * Returns help documentation for a primitive type.
 *
 * @since 1.0.0
 * @category combinators
 */
const getHelp = exports.getHelp = InternalPrimitive.getHelp;
/**
 * Returns a string representation of the primitive type.
 *
 * @since 1.0.0
 * @category combinators
 */
const getTypeName = exports.getTypeName = InternalPrimitive.getTypeName;
/**
 * Represents an integer.
 *
 * @since 1.0.0
 * @category constructors
 */
const integer = exports.integer = InternalPrimitive.integer;
/**
 * Represents a user-defined piece of text.
 *
 * @since 1.0.0
 * @category constructors
 */
const text = exports.text = InternalPrimitive.text;
/**
 * Validates that the specified value, if any, matches the specified primitive
 * type.
 *
 * @since 1.0.0
 * @category combinators
 */
const validate = exports.validate = InternalPrimitive.validate;
/**
 * Runs a wizard that will prompt the user for input matching the specified
 * primitive type.
 *
 * @since 1.0.0
 * @category combinators
 */
const wizard = exports.wizard = InternalPrimitive.wizard;
//# sourceMappingURL=Primitive.js.map