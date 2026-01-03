/**
 * @since 1.0.0
 */
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import { Api } from "./HttpApi.js";
import { Router } from "./HttpApiBuilder.js";
import * as HttpLayerRouter from "./HttpLayerRouter.js";
import * as HttpServerResponse from "./HttpServerResponse.js";
import * as Html from "./internal/html.js";
import * as internal from "./internal/httpApiScalar.js";
import * as OpenApi from "./OpenApi.js";
const makeHandler = options => {
  const spec = OpenApi.fromApi(options.api);
  const source = options?.source;
  const scalarConfig = {
    _integration: "html",
    ...options?.scalar
  };
  const response = HttpServerResponse.html(`<!doctype html>
<html>
  <head>
    <meta charset="utf-8" />
    <title>${Html.escape(spec.info.title)}</title>
    ${!spec.info.description ? "" : `<meta name="description" content="${Html.escape(spec.info.description)}"/>`}
    ${!spec.info.description ? "" : `<meta name="og:description" content="${Html.escape(spec.info.description)}"/>`}
    <meta
      name="viewport"
      content="width=device-width, initial-scale=1" />
  </head>
  <body>
    <script id="api-reference" type="application/json">
      ${Html.escapeJson(spec)}
    </script>
    <script>
      document.getElementById('api-reference').dataset.configuration = JSON.stringify(${Html.escapeJson(scalarConfig)})
    </script>
    ${source._tag === "Cdn" ? `<script src="${`https://cdn.jsdelivr.net/npm/@scalar/api-reference@${source.version ?? "latest"}/dist/browser/standalone.min.js`}" crossorigin></script>` : `<script>${source.source}</script>`}
  </body>
</html>`);
  return Effect.succeed(response);
};
/**
 * @since 1.0.0
 * @category layers
 */
export const layer = options => Router.use(Effect.fnUntraced(function* (router) {
  const {
    api
  } = yield* Api;
  const handler = makeHandler({
    ...options,
    api,
    source: {
      _tag: "Inline",
      source: internal.javascript
    }
  });
  yield* router.get(options?.path ?? "/docs", handler);
}));
/**
 * @since 1.0.0
 * @category layers
 */
export const layerCdn = options => Router.use(Effect.fnUntraced(function* (router) {
  const {
    api
  } = yield* Api;
  const handler = makeHandler({
    ...options,
    api,
    source: {
      _tag: "Cdn",
      version: options?.version
    }
  });
  yield* router.get(options?.path ?? "/docs", handler);
}));
/**
 * @since 1.0.0
 * @category layers
 */
export const layerHttpLayerRouter = /*#__PURE__*/Effect.fnUntraced(function* (options) {
  const router = yield* HttpLayerRouter.HttpRouter;
  const handler = makeHandler({
    ...options,
    source: {
      _tag: "Inline",
      source: internal.javascript
    }
  });
  yield* router.add("GET", options.path, handler);
}, Layer.effectDiscard);
/**
 * @since 1.0.0
 * @category layers
 */
export const layerHttpLayerRouterCdn = /*#__PURE__*/Effect.fnUntraced(function* (options) {
  const router = yield* HttpLayerRouter.HttpRouter;
  const handler = makeHandler({
    ...options,
    source: {
      _tag: "Cdn",
      version: options?.version
    }
  });
  yield* router.add("GET", options.path, handler);
}, Layer.effectDiscard);
//# sourceMappingURL=HttpApiScalar.js.map