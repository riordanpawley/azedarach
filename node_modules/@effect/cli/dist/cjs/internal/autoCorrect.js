"use strict";

Object.defineProperty(exports, "__esModule", {
  value: true
});
exports.levensteinDistance = void 0;
var cliConfig = _interopRequireWildcard(require("./cliConfig.js"));
function _interopRequireWildcard(e, t) { if ("function" == typeof WeakMap) var r = new WeakMap(), n = new WeakMap(); return (_interopRequireWildcard = function (e, t) { if (!t && e && e.__esModule) return e; var o, i, f = { __proto__: null, default: e }; if (null === e || "object" != typeof e && "function" != typeof e) return f; if (o = t ? n : r) { if (o.has(e)) return o.get(e); o.set(e, f); } for (const t in e) "default" !== t && {}.hasOwnProperty.call(e, t) && ((i = (o = Object.defineProperty) && Object.getOwnPropertyDescriptor(e, t)) && (i.get || i.set) ? o(f, t, i) : f[t] = e[t]); return f; })(e, t); }
/** @internal */
const levensteinDistance = (first, second, config) => {
  if (first.length === 0 && second.length === 0) {
    return 0;
  }
  if (first.length === 0) {
    return second.length;
  }
  if (second.length === 0) {
    return first.length;
  }
  const rowCount = first.length;
  const columnCount = second.length;
  const matrix = new Array(rowCount);
  const normalFirst = cliConfig.normalizeCase(config, first);
  const normalSecond = cliConfig.normalizeCase(config, second);
  // Increment each row in the first column
  for (let x = 0; x <= rowCount; x++) {
    matrix[x] = new Array(columnCount);
    matrix[x][0] = x;
  }
  // Increment each column in the first row
  for (let y = 0; y <= columnCount; y++) {
    matrix[0][y] = y;
  }
  // Fill in the rest of the matrix
  for (let row = 1; row <= rowCount; row++) {
    for (let col = 1; col <= columnCount; col++) {
      const cost = normalFirst.charAt(row - 1) === normalSecond.charAt(col - 1) ? 0 : 1;
      matrix[row][col] = Math.min(matrix[row][col - 1] + 1, Math.min(matrix[row - 1][col] + 1, matrix[row - 1][col - 1] + cost));
    }
  }
  return matrix[rowCount][columnCount];
};
exports.levensteinDistance = levensteinDistance;
//# sourceMappingURL=autoCorrect.js.map