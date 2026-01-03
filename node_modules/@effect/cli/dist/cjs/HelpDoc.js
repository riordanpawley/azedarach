"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.toAnsiText = exports.toAnsiDoc = exports.sequence = exports.p = exports.orElse = exports.mapDescriptionList = exports.isSequence = exports.isParagraph = exports.isHeader = exports.isEnumeration = exports.isEmpty = exports.isDescriptionList = exports.h3 = exports.h2 = exports.h1 = exports.getSpan = exports.enumeration = exports.empty = exports.descriptionList = exports.blocks = void 0;
var InternalHelpDoc = _interopRequireWildcard(require("./internal/helpDoc.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 * @category refinements
 */
const isEmpty = exports.isEmpty = InternalHelpDoc.isEmpty;
/**
 * @since 1.0.0
 * @category refinements
 */
const isHeader = exports.isHeader = InternalHelpDoc.isHeader;
/**
 * @since 1.0.0
 * @category refinements
 */
const isParagraph = exports.isParagraph = InternalHelpDoc.isParagraph;
/**
 * @since 1.0.0
 * @category refinements
 */
const isDescriptionList = exports.isDescriptionList = InternalHelpDoc.isDescriptionList;
/**
 * @since 1.0.0
 * @category refinements
 */
const isEnumeration = exports.isEnumeration = InternalHelpDoc.isEnumeration;
/**
 * @since 1.0.0
 * @category refinements
 */
const isSequence = exports.isSequence = InternalHelpDoc.isSequence;
/**
 * @since 1.0.0
 * @category constructors
 */
const empty = exports.empty = InternalHelpDoc.empty;
/**
 * @since 1.0.0
 * @category constructors
 */
const blocks = exports.blocks = InternalHelpDoc.blocks;
/**
 * @since 1.0.0
 * @category constructors
 */
const h1 = exports.h1 = InternalHelpDoc.h1;
/**
 * @since 1.0.0
 * @category constructors
 */
const h2 = exports.h2 = InternalHelpDoc.h2;
/**
 * @since 1.0.0
 * @category constructors
 */
const h3 = exports.h3 = InternalHelpDoc.h3;
/**
 * @since 1.0.0
 * @category constructors
 */
const p = exports.p = InternalHelpDoc.p;
/**
 * @since 1.0.0
 * @category constructors
 */
const descriptionList = exports.descriptionList = InternalHelpDoc.descriptionList;
/**
 * @since 1.0.0
 * @category constructors
 */
const enumeration = exports.enumeration = InternalHelpDoc.enumeration;
/**
 * @since 1.0.0
 * @category getters
 */
const getSpan = exports.getSpan = InternalHelpDoc.getSpan;
/**
 * @since 1.0.0
 * @category combinators
 */
const sequence = exports.sequence = InternalHelpDoc.sequence;
/**
 * @since 1.0.0
 * @category combinators
 */
const orElse = exports.orElse = InternalHelpDoc.orElse;
/**
 * @since 1.0.0
 * @category mapping
 */
const mapDescriptionList = exports.mapDescriptionList = InternalHelpDoc.mapDescriptionList;
/**
 * @since 1.0.0
 * @category rendering
 */
const toAnsiDoc = exports.toAnsiDoc = InternalHelpDoc.toAnsiDoc;
/**
 * @since 1.0.0
 * @category rendering
 */
const toAnsiText = exports.toAnsiText = InternalHelpDoc.toAnsiText;
//# sourceMappingURL=HelpDoc.js.map