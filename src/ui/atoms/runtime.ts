/**
 * Runtime and layer setup for Azedarach atoms
 *
 * Creates the appRuntime that all other atoms use for Effect integration.
 */

import { PlatformLogger } from "@effect/platform"
import { BunContext } from "@effect/platform-bun"
import { Atom } from "@effect-atom/atom"
import { Layer, Logger } from "effect"
import { AppConfig } from "../../config/index.js"
import { AttachmentService } from "../../core/AttachmentService.js"
import { BeadEditorService } from "../../core/BeadEditorService.js"
import { BeadsClient } from "../../core/BeadsClient.js"
import { HookReceiver } from "../../core/HookReceiver.js"
import { ImageAttachmentService } from "../../core/ImageAttachmentService.js"
import { PRWorkflow } from "../../core/PRWorkflow.js"
import { PTYMonitor } from "../../core/PTYMonitor.js"
import { SessionManager } from "../../core/SessionManager.js"
import { TemplateService } from "../../core/TemplateService.js"
import { TerminalService } from "../../core/TerminalService.js"
import { TmuxService } from "../../core/TmuxService.js"
import { VCService } from "../../core/VCService.js"
import { BoardService } from "../../services/BoardService.js"
import { ClockService } from "../../services/ClockService.js"
import { CommandQueueService } from "../../services/CommandQueueService.js"
import { DiagnosticsService } from "../../services/DiagnosticsService.js"
import { EditorService } from "../../services/EditorService.js"
import { KeyboardService } from "../../services/KeyboardService.js"
import { NavigationService } from "../../services/NavigationService.js"
import { OverlayService } from "../../services/OverlayService.js"
import { ProjectService } from "../../services/ProjectService.js"
import { SessionService } from "../../services/SessionService.js"
import { ToastService } from "../../services/ToastService.js"
import { ViewService } from "../../services/ViewService.js"

const platformLayer = BunContext.layer

const fileLogger = Logger.logfmtLogger.pipe(PlatformLogger.toFile("az.log", { flag: "a" }))
const appLayer = Layer.mergeAll(
	SessionService.Default,
	AttachmentService.Default,
	ImageAttachmentService.Default,
	BoardService.Default,
	ClockService.Default,
	TmuxService.Default,
	BeadEditorService.Default,
	PRWorkflow.Default,
	TerminalService.Default,
	EditorService.Default,
	KeyboardService.Default,
	OverlayService.Default,
	ToastService.Default,
	NavigationService.Default,
	SessionManager.Default,
	BeadsClient.Default,
	AppConfig.Default,
	VCService.Default,
	ViewService.Default,
	HookReceiver.Default,
	CommandQueueService.Default,
	PTYMonitor.Default,
	DiagnosticsService.Default,
	ProjectService.Default,
	TemplateService.Default,
).pipe(
	Layer.provide(Logger.replaceScoped(Logger.defaultLogger, fileLogger)),
	Layer.provideMerge(platformLayer),
)

/**
 * Runtime atom that provides all services and platform dependencies
 *
 * This creates a runtime that all other async atoms can use.
 */
export const appRuntime = Atom.runtime(appLayer)
