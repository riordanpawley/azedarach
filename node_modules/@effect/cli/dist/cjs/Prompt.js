"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.toggle = exports.text = exports.succeed = exports.select = exports.run = exports.password = exports.multiSelect = exports.map = exports.list = exports.integer = exports.hidden = exports.float = exports.flatMap = exports.file = exports.date = exports.custom = exports.confirm = exports.all = exports.PromptTypeId = void 0;
var InternalPrompt = _interopRequireWildcard(require("./internal/prompt.js"));
var InternalConfirmPrompt = _interopRequireWildcard(require("./internal/prompt/confirm.js"));
var InternalDatePrompt = _interopRequireWildcard(require("./internal/prompt/date.js"));
var InternalFilePrompt = _interopRequireWildcard(require("./internal/prompt/file.js"));
var InternalListPrompt = _interopRequireWildcard(require("./internal/prompt/list.js"));
var InternalMultiSelectPrompt = _interopRequireWildcard(require("./internal/prompt/multi-select.js"));
var InternalNumberPrompt = _interopRequireWildcard(require("./internal/prompt/number.js"));
var InternalSelectPrompt = _interopRequireWildcard(require("./internal/prompt/select.js"));
var InternalTextPrompt = _interopRequireWildcard(require("./internal/prompt/text.js"));
var InternalTogglePrompt = _interopRequireWildcard(require("./internal/prompt/toggle.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 * @category symbols
 */
const PromptTypeId = exports.PromptTypeId = InternalPrompt.PromptTypeId;
/**
 * Runs all the provided prompts in sequence respecting the structure provided
 * in input.
 *
 * Supports either a tuple / iterable of prompts or a record / struct of prompts
 * as an argument.
 *
 * **Example**
 *
 * ```ts
 * import * as Prompt from "@effect/cli/Prompt"
 * import * as Effect from "effect/Effect"
 *
 * const username = Prompt.text({
 *   message: "Enter your username: "
 * })
 *
 * const password = Prompt.password({
 *   message: "Enter your password: ",
 *   validate: (value) =>
 *     value.length === 0
 *       ? Effect.fail("Password cannot be empty")
 *       : Effect.succeed(value)
 * })
 *
 * const allWithTuple = Prompt.all([username, password])
 *
 * const allWithRecord = Prompt.all({ username, password })
 * ```
 *
 * @since 1.0.0
 * @category collecting & elements
 */
const all = exports.all = InternalPrompt.all;
/**
 * @since 1.0.0
 * @category constructors
 */
const confirm = exports.confirm = InternalConfirmPrompt.confirm;
/**
 * Creates a custom `Prompt` from the specified initial state and handlers.
 *
 * The initial state can either be a pure value or an `Effect`. This is
 * particularly useful when the initial state of the `Prompt` must be computed
 * by performing some effectful computation, such as reading data from the file
 * system.
 *
 * A `Prompt` is essentially a render loop where user input triggers a new frame
 * to be rendered to the `Terminal`. The `handlers` of a custom prompt are used
 * to control what is rendered to the `Terminal` each frame. During each frame,
 * the following occurs:
 *
 *   1. The `render` handler is called with this frame's prompt state and prompt
 *      action and returns an ANSI escape string to be rendered to the
 *      `Terminal`
 *   2. The `Terminal` obtains input from the user
 *   3. The `process` handler is called with the input obtained from the user
 *      and this frame's prompt state and returns the next prompt action that
 *      should be performed
 *   4. The `clear` handler is called with this frame's prompt state and prompt
 *      action and returns an ANSI escape string used to clear the screen of
 *      the `Terminal`
 *
 * @since 1.0.0
 * @category constructors
 */
const custom = exports.custom = InternalPrompt.custom;
/**
 * @since 1.0.0
 * @category constructors
 */
const date = exports.date = InternalDatePrompt.date;
/**
 * @since 1.0.0
 * @category constructors
 */
const file = exports.file = InternalFilePrompt.file;
/**
 * @since 1.0.0
 * @category combinators
 */
const flatMap = exports.flatMap = InternalPrompt.flatMap;
/**
 * @since 1.0.0
 * @category constructors
 */
const float = exports.float = InternalNumberPrompt.float;
/**
 * @since 1.0.0
 * @category constructors
 */
const hidden = exports.hidden = InternalTextPrompt.hidden;
/**
 * @since 1.0.0
 * @category constructors
 */
const integer = exports.integer = InternalNumberPrompt.integer;
/**
 * @since 1.0.0
 * @category constructors
 */
const list = exports.list = InternalListPrompt.list;
/**
 * @since 1.0.0
 * @category combinators
 */
const map = exports.map = InternalPrompt.map;
/**
 * @since 1.0.0
 * @category constructors
 */
const password = exports.password = InternalTextPrompt.password;
/**
 * Executes the specified `Prompt`.
 *
 * @since 1.0.0
 * @category execution
 */
const run = exports.run = InternalPrompt.run;
/**
 * @since 1.0.0
 * @category constructors
 */
const select = exports.select = InternalSelectPrompt.select;
/**
 * @since 1.0.0
 * @category constructors
 */
const multiSelect = exports.multiSelect = InternalMultiSelectPrompt.multiSelect;
/**
 * Creates a `Prompt` which immediately succeeds with the specified value.
 *
 * **NOTE**: This method will not attempt to obtain user input or render
 * anything to the screen.
 *
 * @since 1.0.0
 * @category constructors
 */
const succeed = exports.succeed = InternalPrompt.succeed;
/**
 * @since 1.0.0
 * @category constructors
 */
const text = exports.text = InternalTextPrompt.text;
/**
 * @since 1.0.0
 * @category constructors
 */
const toggle = exports.toggle = InternalTogglePrompt.toggle;
//# sourceMappingURL=Prompt.js.map