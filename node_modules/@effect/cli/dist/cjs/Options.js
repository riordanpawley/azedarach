"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.wizard = exports.withSchema = exports.withPseudoName = exports.withFallbackPrompt = exports.withFallbackConfig = exports.withDescription = exports.withDefault = exports.withAlias = exports.text = exports.secret = exports.repeated = exports.redacted = exports.processCommandLine = exports.parse = exports.orElseEither = exports.orElse = exports.optional = exports.none = exports.mapTryCatch = exports.mapEffect = exports.map = exports.keyValueMap = exports.isOptions = exports.isBool = exports.integer = exports.getUsage = exports.getIdentifier = exports.getHelp = exports.float = exports.filterMap = exports.fileText = exports.fileSchema = exports.fileParse = exports.fileContent = exports.file = exports.directory = exports.date = exports.choiceWithValue = exports.choice = exports.boolean = exports.between = exports.atMost = exports.atLeast = exports.all = exports.OptionsTypeId = void 0;
var InternalOptions = _interopRequireWildcard(require("./internal/options.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 * @category symbols
 */
const OptionsTypeId = exports.OptionsTypeId = InternalOptions.OptionsTypeId;
// =============================================================================
// Refinements
// =============================================================================
/**
 * @since 1.0.0
 * @category refinements
 */
const isOptions = exports.isOptions = InternalOptions.isOptions;
// =============================================================================
// Constructors
// =============================================================================
/**
 * @since 1.0.0
 * @category constructors
 */
const all = exports.all = InternalOptions.all;
/**
 * @since 1.0.0
 * @category constructors
 */
const boolean = exports.boolean = InternalOptions.boolean;
/**
 * Constructs command-line `Options` that represent a choice between several
 * inputs. The input will be mapped to it's associated value during parsing.
 *
 * **Example**
 *
 * ```ts
 * import * as Options from "@effect/cli/Options"
 *
 * export const animal: Options.Options<"dog" | "cat"> = Options.choice(
 *   "animal",
 *   ["dog", "cat"]
 * )
 * ```
 *
 * @since 1.0.0
 * @category constructors
 */
const choice = exports.choice = InternalOptions.choice;
/**
 * Constructs command-line `Options` that represent a choice between several
 * inputs. The input will be mapped to it's associated value during parsing.
 *
 * **Example**
 *
 * ```ts
 * import * as Options from "@effect/cli/Options"
 * import * as Data from "effect/Data"
 *
 * export type Animal = Dog | Cat
 *
 * export interface Dog {
 *   readonly _tag: "Dog"
 * }
 *
 * export const Dog = Data.tagged<Dog>("Dog")
 *
 * export interface Cat {
 *   readonly _tag: "Cat"
 * }
 *
 * export const Cat = Data.tagged<Cat>("Cat")
 *
 * export const animal: Options.Options<Animal> = Options.choiceWithValue("animal", [
 *   ["dog", Dog()],
 *   ["cat", Cat()],
 * ])
 * ```
 *
 * @since 1.0.0
 * @category constructors
 */
const choiceWithValue = exports.choiceWithValue = InternalOptions.choiceWithValue;
/**
 * @since 1.0.0
 * @category constructors
 */
const date = exports.date = InternalOptions.date;
/**
 * Creates a parameter expecting path to a directory.
 *
 * @since 1.0.0
 * @category constructors
 */
const directory = exports.directory = InternalOptions.directory;
/**
 * Creates a parameter expecting path to a file.
 *
 * @since 1.0.0
 * @category constructors
 */
const file = exports.file = InternalOptions.file;
/**
 * Creates a parameter expecting path to a file and reads its contents.
 *
 * @since 1.0.0
 * @category constructors
 */
const fileContent = exports.fileContent = InternalOptions.fileContent;
/**
 * Creates a parameter expecting path to a file and parse its contents.
 *
 * @since 1.0.0
 * @category constructors
 */
const fileParse = exports.fileParse = InternalOptions.fileParse;
/**
 * Creates a parameter expecting path to a file, parse its contents and validate
 * it with a Schema.
 *
 * @since 1.0.0
 * @category constructors
 */
const fileSchema = exports.fileSchema = InternalOptions.fileSchema;
/**
 * Creates a parameter expecting path to a file and reads its contents.
 *
 * @since 1.0.0
 * @category constructors
 */
const fileText = exports.fileText = InternalOptions.fileText;
/**
 * @since 1.0.0
 * @category constructors
 */
const float = exports.float = InternalOptions.float;
/**
 * @since 1.0.0
 * @category combinators
 */
const getHelp = exports.getHelp = InternalOptions.getHelp;
/**
 * @since 1.0.0
 * @category combinators
 */
const getIdentifier = exports.getIdentifier = InternalOptions.getIdentifier;
/**
 * @since 1.0.0
 * @category combinators
 */
const getUsage = exports.getUsage = InternalOptions.getUsage;
/**
 * @since 1.0.0
 * @category constructors
 */
const integer = exports.integer = InternalOptions.integer;
/**
 * @since 1.0.0
 * @category constructors
 */
const keyValueMap = exports.keyValueMap = InternalOptions.keyValueMap;
/**
 * @since 1.0.0
 * @category constructors
 */
const none = exports.none = InternalOptions.none;
/**
 * @since 1.0.0
 * @category constructors
 */
const redacted = exports.redacted = InternalOptions.redacted;
/**
 * @since 1.0.0
 * @category constructors
 * @deprecated
 */
const secret = exports.secret = InternalOptions.secret;
/**
 * @since 1.0.0
 * @category constructors
 */
const text = exports.text = InternalOptions.text;
// =============================================================================
// Combinators
// =============================================================================
/**
 * @since 1.0.0
 * @category combinators
 */
const atMost = exports.atMost = InternalOptions.atMost;
/**
 * @since 1.0.0
 * @category combinators
 */
const atLeast = exports.atLeast = InternalOptions.atLeast;
/**
 * @since 1.0.0
 * @category combinators
 */
const between = exports.between = InternalOptions.between;
/**
 * @since 1.0.0
 * @category combinators
 */
const filterMap = exports.filterMap = InternalOptions.filterMap;
/**
 * Returns `true` if the specified `Options` is a boolean flag, `false`
 * otherwise.
 *
 * @since 1.0.0
 * @category combinators
 */
const isBool = exports.isBool = InternalOptions.isBool;
/**
 * @since 1.0.0
 * @category combinators
 */
const map = exports.map = InternalOptions.map;
/**
 * @since 1.0.0
 * @category combinators
 */
const mapEffect = exports.mapEffect = InternalOptions.mapEffect;
/**
 * @since 1.0.0
 * @category combinators
 */
const mapTryCatch = exports.mapTryCatch = InternalOptions.mapTryCatch;
/**
 * @since 1.0.0
 * @category combinators
 */
const optional = exports.optional = InternalOptions.optional;
/**
 * @since 1.0.0
 * @category combinators
 */
const orElse = exports.orElse = InternalOptions.orElse;
/**
 * @since 1.0.0
 * @category combinators
 */
const orElseEither = exports.orElseEither = InternalOptions.orElseEither;
/**
 * @since 1.0.0
 * @category combinators
 */
const parse = exports.parse = InternalOptions.parse;
/**
 * Indicates that the specified command-line option can be repeated `0` or more
 * times.
 *
 * **NOTE**: if the command-line option is not provided, and empty array will be
 * returned as the value for said option.
 *
 * @since 1.0.0
 * @category combinators
 */
const repeated = exports.repeated = InternalOptions.repeated;
/**
 * Processes the provided command-line arguments, searching for the specified
 * `Options`.
 *
 * Returns an `Option<ValidationError>`, any leftover arguments, and the
 * constructed value of type `A`. The possible error inside
 * `Option<ValidationError>` would only be triggered if there is an error when
 * parsing the command-line arguments. This is because `ValidationError`s are
 * also used internally to control the end of the command-line arguments (i.e.
 * the command-line symbol `--`) corresponding to options.
 *
 * @since 1.0.0
 * @category combinators
 */
const processCommandLine = exports.processCommandLine = InternalOptions.processCommandLine;
/**
 * @since 1.0.0
 * @category combinators
 */
const withAlias = exports.withAlias = InternalOptions.withAlias;
/**
 * @since 1.0.0
 * @category combinators
 */
const withDefault = exports.withDefault = InternalOptions.withDefault;
/**
 * @since 1.0.0
 * @category combinators
 */
const withFallbackConfig = exports.withFallbackConfig = InternalOptions.withFallbackConfig;
/**
 * @since 1.0.0
 * @category combinators
 */
const withFallbackPrompt = exports.withFallbackPrompt = InternalOptions.withFallbackPrompt;
/**
 * @since 1.0.0
 * @category combinators
 */
const withDescription = exports.withDescription = InternalOptions.withDescription;
/**
 * @since 1.0.0
 * @category combinators
 */
const withPseudoName = exports.withPseudoName = InternalOptions.withPseudoName;
/**
 * @since 1.0.0
 * @category combinators
 */
const withSchema = exports.withSchema = InternalOptions.withSchema;
/**
 * @since 1.0.0
 * @category combinators
 */
const wizard = exports.wizard = InternalOptions.wizard;
//# sourceMappingURL=Options.js.map