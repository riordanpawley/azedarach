"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.wrap = exports.uninterruptible = exports.make = exports.isWrapper = exports.isRpc = exports.fromTaggedRequest = exports.fork = exports.exitSchema = exports.WrapperTypeId = exports.TypeId = void 0;
var Context_ = _interopRequireWildcard(require("effect/Context"));
var _GlobalValue = require("effect/GlobalValue");
var Option = _interopRequireWildcard(require("effect/Option"));
var _Pipeable = require("effect/Pipeable");
var Predicate = _interopRequireWildcard(require("effect/Predicate"));
var PrimaryKey = _interopRequireWildcard(require("effect/PrimaryKey"));
var Schema = _interopRequireWildcard(require("effect/Schema"));
var RpcSchema = _interopRequireWildcard(require("./RpcSchema.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/**
 * @since 1.0.0
 * @category type ids
 */
const TypeId = exports.TypeId = /*#__PURE__*/Symbol.for("@effect/rpc/Rpc");
/**
 * @since 1.0.0
 * @category guards
 */
const isRpc = u => Predicate.hasProperty(u, TypeId);
exports.isRpc = isRpc;
const Proto = {
  [TypeId]: TypeId,
  pipe() {
    return (0, _Pipeable.pipeArguments)(this, arguments);
  },
  setSuccess(successSchema) {
    return makeProto({
      _tag: this._tag,
      payloadSchema: this.payloadSchema,
      successSchema,
      errorSchema: this.errorSchema,
      annotations: this.annotations,
      middlewares: this.middlewares
    });
  },
  setError(errorSchema) {
    return makeProto({
      _tag: this._tag,
      payloadSchema: this.payloadSchema,
      successSchema: this.successSchema,
      errorSchema,
      annotations: this.annotations,
      middlewares: this.middlewares
    });
  },
  setPayload(payloadSchema) {
    return makeProto({
      _tag: this._tag,
      payloadSchema: Schema.isSchema(payloadSchema) ? payloadSchema : Schema.Struct(payloadSchema),
      successSchema: this.successSchema,
      errorSchema: this.errorSchema,
      annotations: this.annotations,
      middlewares: this.middlewares
    });
  },
  middleware(middleware) {
    return makeProto({
      _tag: this._tag,
      payloadSchema: this.payloadSchema,
      successSchema: this.successSchema,
      errorSchema: this.errorSchema,
      annotations: this.annotations,
      middlewares: new Set([...this.middlewares, middleware])
    });
  },
  prefix(prefix) {
    return makeProto({
      _tag: `${prefix}${this._tag}`,
      payloadSchema: this.payloadSchema,
      successSchema: this.successSchema,
      errorSchema: this.errorSchema,
      annotations: this.annotations,
      middlewares: this.middlewares
    });
  },
  annotate(tag, value) {
    return makeProto({
      _tag: this._tag,
      payloadSchema: this.payloadSchema,
      successSchema: this.successSchema,
      errorSchema: this.errorSchema,
      middlewares: this.middlewares,
      annotations: Context_.add(this.annotations, tag, value)
    });
  },
  annotateContext(context) {
    return makeProto({
      _tag: this._tag,
      payloadSchema: this.payloadSchema,
      successSchema: this.successSchema,
      errorSchema: this.errorSchema,
      middlewares: this.middlewares,
      annotations: Context_.merge(this.annotations, context)
    });
  }
};
const makeProto = options => {
  function Rpc() {}
  Object.setPrototypeOf(Rpc, Proto);
  Object.assign(Rpc, options);
  Rpc.key = `@effect/rpc/Rpc/${options._tag}`;
  return Rpc;
};
/**
 * @since 1.0.0
 * @category constructors
 */
const make = (tag, options) => {
  const successSchema = options?.success ?? Schema.Void;
  const errorSchema = options?.error ?? Schema.Never;
  let payloadSchema;
  if (options?.primaryKey) {
    payloadSchema = class Payload extends Schema.Class(`@effect/rpc/Rpc/${tag}`)(options.payload) {
      [PrimaryKey.symbol]() {
        return options.primaryKey(this);
      }
    };
  } else {
    payloadSchema = Schema.isSchema(options?.payload) ? options?.payload : options?.payload ? Schema.Struct(options?.payload) : Schema.Void;
  }
  return makeProto({
    _tag: tag,
    payloadSchema,
    successSchema: options?.stream ? RpcSchema.Stream({
      success: successSchema,
      failure: errorSchema
    }) : successSchema,
    errorSchema: options?.stream ? Schema.Never : errorSchema,
    annotations: Context_.empty(),
    middlewares: new Set()
  });
};
/**
 * @since 1.0.0
 * @category constructors
 */
exports.make = make;
const fromTaggedRequest = schema => makeProto({
  _tag: schema._tag,
  payloadSchema: schema,
  successSchema: schema.success,
  errorSchema: schema.failure,
  annotations: Context_.empty(),
  middlewares: new Set()
});
exports.fromTaggedRequest = fromTaggedRequest;
const exitSchemaCache = /*#__PURE__*/(0, _GlobalValue.globalValue)("@effect/rpc/Rpc/exitSchemaCache", () => new WeakMap());
/**
 * @since 1.0.0
 * @category constructors
 */
const exitSchema = self => {
  if (exitSchemaCache.has(self)) {
    return exitSchemaCache.get(self);
  }
  const rpc = self;
  const failures = new Set([rpc.errorSchema]);
  const streamSchemas = RpcSchema.getStreamSchemas(rpc.successSchema.ast);
  if (Option.isSome(streamSchemas)) {
    failures.add(streamSchemas.value.failure);
  }
  for (const middleware of rpc.middlewares) {
    failures.add(middleware.failure);
  }
  const schema = Schema.Exit({
    success: Option.isSome(streamSchemas) ? Schema.Void : rpc.successSchema,
    failure: Schema.Union(...failures),
    defect: Schema.Defect
  });
  exitSchemaCache.set(self, schema);
  return schema;
};
/**
 * @since 1.0.0
 * @category Wrapper
 */
exports.exitSchema = exitSchema;
const WrapperTypeId = exports.WrapperTypeId = /*#__PURE__*/Symbol.for("@effect/rpc/Rpc/Wrapper");
/**
 * @since 1.0.0
 * @category Wrapper
 */
const isWrapper = u => WrapperTypeId in u;
/**
 * @since 1.0.0
 * @category Wrapper
 */
exports.isWrapper = isWrapper;
const wrap = options => value => isWrapper(value) ? {
  [WrapperTypeId]: WrapperTypeId,
  value: value.value,
  fork: options.fork ?? value.fork,
  uninterruptible: options.uninterruptible ?? value.uninterruptible
} : {
  [WrapperTypeId]: WrapperTypeId,
  value,
  fork: options.fork ?? false,
  uninterruptible: options.uninterruptible ?? false
};
/**
 * You can use `fork` to wrap a response Effect or Stream, to ensure that the
 * response is executed concurrently regardless of the RpcServer concurrency
 * setting.
 *
 * @since 1.0.0
 * @category Wrapper
 */
exports.wrap = wrap;
const fork = exports.fork = /*#__PURE__*/wrap({
  fork: true
});
/**
 * You can use `uninterruptible` to wrap a response Effect or Stream, to ensure
 * that it is executed inside an uninterruptible region.
 *
 * @since 1.0.0
 * @category Wrapper
 */
const uninterruptible = exports.uninterruptible = /*#__PURE__*/wrap({
  uninterruptible: true
});
//# sourceMappingURL=Rpc.js.map