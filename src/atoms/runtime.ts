/**
 * Mode service re-export
 *
 * Re-exports EditorService under the name ModeService to avoid
 * naming collision with BeadEditorService (which handles external $EDITOR).
 *
 * EditorService manages the modal editing state (normal, select, search, etc.)
 * and is aliased under this name throughout the UI layer.
 */

// Alias to avoid collision with core/BeadEditorService (external $EDITOR)
import { EditorService as ModeService } from "../services/EditorService.js"

export { ModeService }
