"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.unclusteredFlag = exports.noBuiltInMatch = exports.missingValue = exports.missingSubcommand = exports.missingFlag = exports.keyValuesDetected = exports.isValidationError = exports.isUnclusteredFlag = exports.isNoBuiltInMatch = exports.isMultipleValuesDetected = exports.isMissingValue = exports.isMissingSubcommand = exports.isMissingFlag = exports.isInvalidValue = exports.isInvalidArgument = exports.isHelpRequested = exports.isCorrectedFlag = exports.isCommandMismatch = exports.invalidValue = exports.invalidArgument = exports.helpRequested = exports.correctedFlag = exports.commandMismatch = exports.ValidationErrorTypeId = void 0;
var InternalCommand = _interopRequireWildcard(require("./internal/commandDescriptor.js"));
var InternalValidationError = _interopRequireWildcard(require("./internal/validationError.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 * @category symbols
 */
const ValidationErrorTypeId = exports.ValidationErrorTypeId = InternalValidationError.ValidationErrorTypeId;
/**
 * @since 1.0.0
 * @category refinements
 */
const isValidationError = exports.isValidationError = InternalValidationError.isValidationError;
/**
 * @since 1.0.0
 * @category refinements
 */
const isCommandMismatch = exports.isCommandMismatch = InternalValidationError.isCommandMismatch;
/**
 * @since 1.0.0
 * @category refinements
 */
const isCorrectedFlag = exports.isCorrectedFlag = InternalValidationError.isCorrectedFlag;
/**
 * @since 1.0.0
 * @category refinements
 */
const isHelpRequested = exports.isHelpRequested = InternalValidationError.isHelpRequested;
/**
 * @since 1.0.0
 * @category refinements
 */
const isInvalidArgument = exports.isInvalidArgument = InternalValidationError.isInvalidArgument;
/**
 * @since 1.0.0
 * @category refinements
 */
const isInvalidValue = exports.isInvalidValue = InternalValidationError.isInvalidValue;
/**
 * @since 1.0.0
 * @category refinements
 */
const isMultipleValuesDetected = exports.isMultipleValuesDetected = InternalValidationError.isMultipleValuesDetected;
/**
 * @since 1.0.0
 * @category refinements
 */
const isMissingFlag = exports.isMissingFlag = InternalValidationError.isMissingFlag;
/**
 * @since 1.0.0
 * @category refinements
 */
const isMissingValue = exports.isMissingValue = InternalValidationError.isMissingValue;
/**
 * @since 1.0.0
 * @category refinements
 */
const isMissingSubcommand = exports.isMissingSubcommand = InternalValidationError.isMissingSubcommand;
/**
 * @since 1.0.0
 * @category refinements
 */
const isNoBuiltInMatch = exports.isNoBuiltInMatch = InternalValidationError.isNoBuiltInMatch;
/**
 * @since 1.0.0
 * @category refinements
 */
const isUnclusteredFlag = exports.isUnclusteredFlag = InternalValidationError.isUnclusteredFlag;
/**
 * @since 1.0.0
 * @category constructors
 */
const commandMismatch = exports.commandMismatch = InternalValidationError.commandMismatch;
/**
 * @since 1.0.0
 * @category constructors
 */
const correctedFlag = exports.correctedFlag = InternalValidationError.correctedFlag;
/**
 * @since 1.0.0
 * @category constructors
 */
const helpRequested = exports.helpRequested = InternalCommand.helpRequestedError;
/**
 * @since 1.0.0
 * @category constructors
 */
const invalidArgument = exports.invalidArgument = InternalValidationError.invalidArgument;
/**
 * @since 1.0.0
 * @category constructors
 */
const invalidValue = exports.invalidValue = InternalValidationError.invalidValue;
/**
 * @since 1.0.0
 * @category constructors
 */
const keyValuesDetected = exports.keyValuesDetected = InternalValidationError.multipleValuesDetected;
/**
 * @since 1.0.0
 * @category constructors
 */
const missingFlag = exports.missingFlag = InternalValidationError.missingFlag;
/**
 * @since 1.0.0
 * @category constructors
 */
const missingValue = exports.missingValue = InternalValidationError.missingValue;
/**
 * @since 1.0.0
 * @category constructors
 */
const missingSubcommand = exports.missingSubcommand = InternalValidationError.missingSubcommand;
/**
 * @since 1.0.0
 * @category constructors
 */
const noBuiltInMatch = exports.noBuiltInMatch = InternalValidationError.noBuiltInMatch;
/**
 * @since 1.0.0
 * @category constructors
 */
const unclusteredFlag = exports.unclusteredFlag = InternalValidationError.unclusteredFlag;
//# sourceMappingURL=ValidationError.js.map