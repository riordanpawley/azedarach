/**
 * Transitional re-export while legacy root services still depend on the TUI-owned
 * image attachment surface. The live implementation now lives in packages/tui.
 */

export type { ImageAttachment } from "../../packages/tui/src/contracts.js"
export {
	ClipboardError,
	FileNotFoundError,
	ImageAttachmentError,
	ImageAttachmentService,
	type ImageAttachmentServiceApi,
} from "../../packages/tui/src/services/ImageAttachmentService.js"
