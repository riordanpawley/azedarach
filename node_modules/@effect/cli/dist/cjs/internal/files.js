"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.readString = exports.read = exports.parse = exports.fileParsers = void 0;
var FileSystem = _interopRequireWildcard(require("@effect/platform/FileSystem"));
var Effect = _interopRequireWildcard(require("effect/Effect"));
var Ini = _interopRequireWildcard(require("ini"));
var Toml = _interopRequireWildcard(require("toml"));
var Yaml = _interopRequireWildcard(require("yaml"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/** @internal */
const fileParsers = exports.fileParsers = {
  json: content => JSON.parse(content),
  yaml: content => Yaml.parse(content),
  yml: content => Yaml.parse(content),
  ini: content => Ini.parse(content),
  toml: content => Toml.parse(content),
  tml: content => Toml.parse(content)
};
/** @internal */
const read = path => Effect.flatMap(FileSystem.FileSystem, fs => Effect.matchEffect(fs.readFile(path), {
  onFailure: error => Effect.fail(`Could not read file (${path}): ${error}`),
  onSuccess: content => Effect.succeed([path, content])
}));
/** @internal */
exports.read = read;
const readString = path => Effect.flatMap(FileSystem.FileSystem, fs => Effect.matchEffect(fs.readFileString(path), {
  onFailure: error => Effect.fail(`Could not read file (${path}): ${error}`),
  onSuccess: content => Effect.succeed([path, content])
}));
/** @internal */
exports.readString = readString;
const parse = (path, content, format) => {
  const parser = fileParsers[format ?? path.split(".").pop()];
  if (parser === undefined) {
    return Effect.fail(`Unsupported file format: ${format}`);
  }
  return Effect.try({
    try: () => parser(content),
    catch: e => `Could not parse ${format} file (${path}): ${e}`
  });
};
exports.parse = parse;
//# sourceMappingURL=files.js.map