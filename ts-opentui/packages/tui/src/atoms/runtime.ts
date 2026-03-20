/**
 * Runtime and layer setup for Azedarach atoms
 *
 * Creates the appRuntime that all other atoms use for Effect integration.
 */

import {
	AppConfig,
	AppConfigNotifier,
	type AppConfigNotifierApi,
	AppConfigProjectContext,
	type AppConfigProjectContextApi,
} from "@azedarach/config"
import { DaemonRpcClient, type DaemonRpcClientApi } from "@azedarach/shared/rpc"
import { PlatformLogger } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Atom } from "@effect-atom/atom"
import { Data, Effect, Layer, Logger, Stream } from "effect"
import { ClockService } from "../services/ClockService.js"
import { CommandQueueService } from "../services/CommandQueueService.js"
import { DiagnosticsService } from "../services/DiagnosticsService.js"
import { DiffService } from "../services/DiffService.js"
import { EditorService } from "../services/EditorService.js"
import { NetworkService } from "../services/NetworkService.js"
import { OfflineService } from "../services/OfflineService.js"
import { OverlayService } from "../services/OverlayService.js"
import { SettingsService } from "../services/SettingsService.js"
import { ToastService } from "../services/ToastService.js"
import { TuiBoardStoreService } from "../services/TuiBoardStoreService.js"
import { ViewService } from "../services/ViewService.js"
import {
	AttachmentService,
	ImageAttachmentService,
	IssueEditorService,
	KeyboardService,
	NavigationService,
	PlanningService,
	PRWorkflow,
	ProjectService,
	ProjectStateService,
	TemplateService,
	TerminalService,
	TmuxService,
	TmuxSessionMonitor,
	VCService,
} from "../utils/runtimeServices.js"

const platformLayer = BunContext.layer

const fileLogger = Logger.logfmtLogger.pipe(PlatformLogger.toFile("az.log", { flag: "a" }))
const loggerLayer = Logger.replaceScoped(Logger.defaultLogger, fileLogger)
export type TuiRuntimeMode = "daemon-rpc"
export const AZEDARACH_TUI_RUNTIME_MODE_ENV = "AZEDARACH_TUI_RUNTIME_MODE"

class TuiDaemonRpcClientNotConfiguredError extends Data.TaggedError(
	"TuiDaemonRpcClientNotConfiguredError",
)<{
	readonly message: string
}> {}

let configuredTuiDaemonRpcClient: DaemonRpcClientApi | undefined

export const configureTuiDaemonRpcClient = (daemonRpcClient: DaemonRpcClientApi): void => {
	configuredTuiDaemonRpcClient = daemonRpcClient
}

export const resolveTuiRuntimeModeFromEnv = (
	_env: Readonly<Record<string, string | undefined>>,
): TuiRuntimeMode => "daemon-rpc"

const appConfigProjectContextLayer = Layer.effect(
	AppConfigProjectContext,
	Effect.gen(function* () {
		const projectService = yield* ProjectService
		return {
			getCurrentPath: () => projectService.getCurrentPath(),
			currentProjectPathChanges: projectService.currentProject.changes.pipe(
				Stream.map((project) => project?.path),
			),
		} satisfies AppConfigProjectContextApi
	}),
)

const appConfigNotifierLayer = Layer.effect(
	AppConfigNotifier,
	Effect.gen(function* () {
		const toastService = yield* ToastService
		return {
			showError: (message: string) => Effect.asVoid(toastService.show("error", message)),
		} satisfies AppConfigNotifierApi
	}),
)

const daemonRpcClientLayer = Layer.effect(
	DaemonRpcClient,
	Effect.gen(function* () {
		const daemonRpcClient = configuredTuiDaemonRpcClient
		if (daemonRpcClient === undefined) {
			return yield* Effect.fail(
				new TuiDaemonRpcClientNotConfiguredError({
					message: "TUI daemon RPC client must be configured before launch",
				}),
			)
		}
		return daemonRpcClient
	}),
)

const settingsLayer = SettingsService.Default.pipe(Layer.provide(appConfigProjectContextLayer))
const imageAttachmentLayer = ImageAttachmentService.Default.pipe(
	Layer.provideMerge(daemonRpcClientLayer),
	Layer.provideMerge(appConfigProjectContextLayer),
)
const overlayLayer = OverlayService.Default.pipe(Layer.provideMerge(imageAttachmentLayer))
const tuiBoardStoreLayer = TuiBoardStoreService.Default.pipe(
	Layer.provide(appConfigProjectContextLayer),
	Layer.provideMerge(daemonRpcClientLayer),
)
const navigationLayer = NavigationService.Default.pipe(
	Layer.provideMerge(tuiBoardStoreLayer),
	Layer.provideMerge(daemonRpcClientLayer),
)

const coreServicesLayer = Layer.mergeAll(
	AttachmentService.Default,
	overlayLayer,
	imageAttachmentLayer,
	ClockService.Default,
	TmuxService.Default,
	IssueEditorService.Default.pipe(
		Layer.provideMerge(daemonRpcClientLayer),
		Layer.provideMerge(AppConfig.Default),
	),
	PRWorkflow.Default.pipe(
		Layer.provideMerge(daemonRpcClientLayer),
		Layer.provideMerge(appConfigProjectContextLayer),
	),
	TerminalService.Default,
	EditorService.Default,
	KeyboardService.Default.pipe(
		Layer.provideMerge(daemonRpcClientLayer),
		Layer.provideMerge(appConfigProjectContextLayer),
	),
	navigationLayer,
	appConfigProjectContextLayer,
	appConfigNotifierLayer,
	AppConfig.Default,
	daemonRpcClientLayer,
	ViewService.Default,
	CommandQueueService.Default,
	DiagnosticsService.Default,
	ProjectStateService.Default,
	settingsLayer,
	tuiBoardStoreLayer,
	TemplateService.Default,
	NetworkService.Default,
	OfflineService.Default,
	DiffService.Default,
).pipe(Layer.provideMerge(ToastService.Default), Layer.provideMerge(ProjectService.Default))

const deferredServicesLayer = Layer.mergeAll(
	PlanningService.Default.pipe(Layer.provideMerge(daemonRpcClientLayer)),
	TmuxSessionMonitor.Default,
	VCService.Default,
)

/**
 * Core layer for startup-critical services.
 * This is intentionally narrower than the full application graph and can be
 * consumed by atoms that should be available before deferred hydration.
 */
export const appCoreLayer = coreServicesLayer.pipe(
	Layer.provide(loggerLayer),
	Layer.provideMerge(platformLayer),
)

/**
 * Deferred feature layer for non-critical services.
 * Atoms can migrate to appDeferredRuntime incrementally.
 */
const appFullServicesLayer = Layer.mergeAll(coreServicesLayer, deferredServicesLayer)

export const appDeferredLayer = appFullServicesLayer.pipe(
	Layer.provide(loggerLayer),
	Layer.provideMerge(platformLayer),
)

export const resolveAppStartupLayerModeFromEnv = (
	_env: Readonly<Record<string, string | undefined>>,
): "daemon-core" => "daemon-core"

/**
 * Full compatibility layer used by the existing appRuntime surface.
 */
export const appLayer = appDeferredLayer

/**
 * Runtime atom that provides all services and platform dependencies
 *
 * This creates a runtime that all other async atoms can use.
 */

export const appCoreRuntime = Atom.runtime(appCoreLayer)
export const appDeferredRuntime = Atom.runtime(appDeferredLayer)
export const appRuntime = Atom.runtime(appLayer)
