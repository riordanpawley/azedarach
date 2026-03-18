/**
 * Runtime and layer setup for Azedarach atoms
 *
 * Creates the appRuntime that all other atoms use for Effect integration.
 */

import { PlatformLogger } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Atom } from "@effect-atom/atom"
import { Layer, Logger } from "effect"
import { AppConfig } from "../../../../src/config/index.js"
import { AttachmentService } from "../../../../src/core/AttachmentService.js"
import { ImageAttachmentService } from "../../../../src/core/ImageAttachmentService.js"
import { IssueEditorService } from "../../../../src/core/IssueEditorService.js"
import { IssueTrackerClient } from "../../../../src/core/IssueTrackerClient.js"
import { PlanningService } from "../../../../src/core/PlanningService.js"
import { PRWorkflow } from "../../../../src/core/PRWorkflow.js"
import { PTYMonitor } from "../../../../src/core/PTYMonitor.js"
import { SessionManager } from "../../../../src/core/SessionManager.js"
import { SpecService } from "../../../../src/core/SpecService.js"
import { TemplateService } from "../../../../src/core/TemplateService.js"
import { TerminalService } from "../../../../src/core/TerminalService.js"
import { TmuxService } from "../../../../src/core/TmuxService.js"
import { TmuxSessionMonitor } from "../../../../src/core/TmuxSessionMonitor.js"
import { VCService } from "../../../../src/core/VCService.js"
import { BoardService } from "../../../../src/services/BoardService.js"
import { ClockService } from "../../../../src/services/ClockService.js"
import { CommandQueueService } from "../../../../src/services/CommandQueueService.js"
import { DevServerService } from "../../../../src/services/DevServerService.js"
import { DiagnosticsService } from "../../../../src/services/DiagnosticsService.js"
import { DiffService } from "../../../../src/services/DiffService.js"
import { EditorService } from "../../../../src/services/EditorService.js"
import { GitSyncService } from "../../../../src/services/GitSyncService.js"
import { KeyboardService } from "../../../../src/services/KeyboardService.js"
import { MutationQueue } from "../../../../src/services/MutationQueue.js"
import { NavigationService } from "../../../../src/services/NavigationService.js"
import { NetworkService } from "../../../../src/services/NetworkService.js"
import { OfflineService } from "../../../../src/services/OfflineService.js"
import { OverlayService } from "../../../../src/services/OverlayService.js"
import { ProjectService } from "../../../../src/services/ProjectService.js"
import { ProjectStateService } from "../../../../src/services/ProjectStateService.js"
import { SessionService } from "../../../../src/services/SessionService.js"
import { SettingsService } from "../../../../src/services/SettingsService.js"
import { ToastService } from "../../../../src/services/ToastService.js"
import { ViewService } from "../../../../src/services/ViewService.js"

const platformLayer = BunContext.layer

const fileLogger = Logger.logfmtLogger.pipe(PlatformLogger.toFile("az.log", { flag: "a" }))
const loggerLayer = Logger.replaceScoped(Logger.defaultLogger, fileLogger)
export type TuiRuntimeMode = "daemon-rpc"
export const AZEDARACH_TUI_RUNTIME_MODE_ENV = "AZEDARACH_TUI_RUNTIME_MODE"

export const resolveTuiRuntimeModeFromEnv = (
	_env: Readonly<Record<string, string | undefined>>,
): TuiRuntimeMode => "daemon-rpc"

const coreServicesLayer = Layer.mergeAll(
	MutationQueue.Default,
	SessionService.Default,
	AttachmentService.Default,
	OverlayService.Default,
	ImageAttachmentService.Default,
	BoardService.Default,
	ClockService.Default,
	TmuxService.Default,
	IssueEditorService.Default,
	PRWorkflow.Default,
	TerminalService.Default,
	EditorService.Default,
	KeyboardService.Default,
	ToastService.Default,
	NavigationService.Default,
	SessionManager.Default,
	IssueTrackerClient.Default,
	AppConfig.Default,
	ViewService.Default,
	CommandQueueService.Default,
	PTYMonitor.Default,
	DiagnosticsService.Default,
	ProjectService.Default,
	ProjectStateService.Default,
	SettingsService.Default,
	TemplateService.Default,
	NetworkService.Default,
	OfflineService.Default,
	DevServerService.Default,
	DiffService.Default,
	GitSyncService.Default,
)

const deferredServicesLayer = Layer.mergeAll(
	PlanningService.Default,
	SpecService.Default,
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
