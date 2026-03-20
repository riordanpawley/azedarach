// biome-ignore lint/correctness/useImportExtensions: <wtf>
import packageJson from "./package.json" with { type: "json" }

export default packageJson
