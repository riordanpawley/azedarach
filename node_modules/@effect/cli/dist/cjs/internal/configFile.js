"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.makeProvider = exports.layer = exports.ConfigFileError = exports.ConfigErrorTypeId = void 0;
var FileSystem = _interopRequireWildcard(require("@effect/platform/FileSystem"));
var Path = _interopRequireWildcard(require("@effect/platform/Path"));
var Cause = _interopRequireWildcard(require("effect/Cause"));
var ConfigProvider = _interopRequireWildcard(require("effect/ConfigProvider"));
var Context = _interopRequireWildcard(require("effect/Context"));
var DefaultServices = _interopRequireWildcard(require("effect/DefaultServices"));
var Effect = _interopRequireWildcard(require("effect/Effect"));
var _Function = require("effect/Function");
var Layer = _interopRequireWildcard(require("effect/Layer"));
var InternalFiles = _interopRequireWildcard(require("./files.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
const fileExtensions = {
  json: ["json"],
  yaml: ["yaml", "yml"],
  ini: ["ini"],
  toml: ["toml", "tml"]
};
const allFileExtensions = /*#__PURE__*/Object.values(fileExtensions).flat();
/** @internal */
const makeProvider = (fileName, options) => Effect.gen(function* () {
  const path = yield* Path.Path;
  const fs = yield* FileSystem.FileSystem;
  const searchPaths = options?.searchPaths && options.searchPaths.length ? options.searchPaths : ["."];
  const extensions = options?.formats && options.formats.length ? options.formats.flatMap(_ => fileExtensions[_]) : allFileExtensions;
  const filePaths = yield* Effect.filter(searchPaths.flatMap(searchPath => extensions.map(ext => path.join(searchPath, `${fileName}.${ext}`))), path => Effect.orElseSucceed(fs.exists(path), () => false));
  const providers = yield* Effect.forEach(filePaths, path => (0, _Function.pipe)(fs.readFileString(path), Effect.mapError(_ => ConfigFileError(`Could not read file (${path})`)), Effect.flatMap(content => Effect.mapError(InternalFiles.parse(path, content), message => ConfigFileError(message))), Effect.map(data => ConfigProvider.fromJson(data))));
  if (providers.length === 0) {
    return ConfigProvider.fromMap(new Map());
  }
  return providers.reduce((acc, provider) => ConfigProvider.orElse(acc, () => provider));
});
/** @internal */
exports.makeProvider = makeProvider;
const layer = (fileName, options) => (0, _Function.pipe)(makeProvider(fileName, options), Effect.map(provider => Layer.fiberRefLocallyScopedWith(DefaultServices.currentServices, services => {
  const current = Context.get(services, ConfigProvider.ConfigProvider);
  return Context.add(services, ConfigProvider.ConfigProvider, ConfigProvider.orElse(current, () => provider));
})), Layer.unwrapEffect);
/** @internal */
exports.layer = layer;
const ConfigErrorTypeId = exports.ConfigErrorTypeId = /*#__PURE__*/Symbol.for("@effect/cli/ConfigFile/ConfigFileError");
const ConfigFileErrorProto = /*#__PURE__*/Object.assign(/*#__PURE__*/Object.create(Cause.YieldableError.prototype), {
  [ConfigErrorTypeId]: ConfigErrorTypeId
});
/** @internal */
const ConfigFileError = message => {
  const self = Object.create(ConfigFileErrorProto);
  self._tag = "ConfigFileError";
  self.message = message;
  return self;
};
exports.ConfigFileError = ConfigFileError;
//# sourceMappingURL=configFile.js.map