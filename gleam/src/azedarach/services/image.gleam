// Image Attachment Service - manages images separately from beads
//
// Since beads CLI doesn't support file attachments, images are stored in:
// - .beads/images/{issue-id}/*.png (actual files)
// - .beads/images/index.json (metadata mapping)
//
// When attaching, we also update the bead's notes with a markdown link.

import gleam/dynamic/decode.{type Decoder}
import gleam/dict.{type Dict}
import gleam/int
import gleam/json
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/result
import gleam/string
import azedarach/util/shell
import simplifile
import tempo

// Erlang FFI for unique integer generation
@external(erlang, "erlang", "unique_integer")
fn unique_integer() -> Int

// ============================================================================
// Types
// ============================================================================

pub type ImageAttachment {
  ImageAttachment(
    id: String,
    filename: String,
    original_path: String,
    mime_type: String,
    size: Int,
    created_at: String,
  )
}

pub type AttachmentIndex =
  Dict(String, List(ImageAttachment))

pub type ImageError {
  FileNotFound(path: String)
  ClipboardError(message: String, tool: Option(String))
  StorageError(message: String)
  ParseError(message: String)
}

pub fn error_to_string(err: ImageError) -> String {
  case err {
    FileNotFound(path) -> "File not found: " <> path
    ClipboardError(msg, tool) ->
      case tool {
        Some(t) -> "Clipboard error (" <> t <> "): " <> msg
        None -> "Clipboard error: " <> msg
      }
    StorageError(msg) -> "Storage error: " <> msg
    ParseError(msg) -> "Parse error: " <> msg
  }
}

// ============================================================================
// Constants
// ============================================================================

const images_dir = ".beads/images"

const index_file = "index.json"

const image_extensions = [".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg"]

// ============================================================================
// Public API
// ============================================================================

/// List attachments for an issue
pub fn list(issue_id: String) -> Result(List(ImageAttachment), ImageError) {
  case read_index() {
    Ok(index) -> Ok(dict.get(index, issue_id) |> result.unwrap([]))
    Error(e) -> Error(e)
  }
}

/// Attach an image from a file path
pub fn attach_file(
  issue_id: String,
  file_path: String,
) -> Result(ImageAttachment, ImageError) {
  // Verify file exists
  use _ <- guard_file_exists(file_path)

  // Check if it's an image
  let filename = get_basename(file_path)
  use _ <- guard_is_image(filename)

  // Create issue directory
  let issue_dir = images_dir <> "/" <> issue_id
  use _ <- guard_mkdir(issue_dir)

  // Generate unique ID and destination
  let id = generate_id()
  let ext = get_extension(filename)
  let dest_filename = id <> ext
  let dest_path = issue_dir <> "/" <> dest_filename

  // Copy file
  use _ <- guard_copy(file_path, dest_path)

  // Get file size
  let size = case simplifile.file_info(dest_path) {
    Ok(info) -> info.size
    Error(_) -> 0
  }

  // Create attachment metadata
  let attachment =
    ImageAttachment(
      id: id,
      filename: dest_filename,
      original_path: file_path,
      mime_type: get_mime_type(ext),
      size: size,
      created_at: now_iso(),
    )

  // Update index
  update_index(issue_id, attachment)
  |> result.map(fn(_) { attachment })
}

/// Attach image from clipboard
pub fn attach_from_clipboard(issue_id: String) -> Result(ImageAttachment, ImageError) {
  // Detect clipboard tool
  use tool <- guard_clipboard_tool()

  // Create issue directory
  let issue_dir = images_dir <> "/" <> issue_id
  use _ <- guard_mkdir(issue_dir)

  // Generate unique ID
  let id = generate_id()
  let dest_filename = id <> ".png"
  let dest_path = issue_dir <> "/" <> dest_filename

  // Build and execute clipboard command
  let cmd = build_clipboard_command(tool, dest_path)
  use _ <- guard_clipboard_paste(cmd, tool)

  // Verify file was created
  use _ <- guard_clipboard_file(dest_path, tool)

  // Get file size and verify not empty
  let size = case simplifile.file_info(dest_path) {
    Ok(info) -> info.size
    Error(_) -> 0
  }

  use _ <- guard_clipboard_size(size, dest_path, tool)

  // Create attachment
  let attachment =
    ImageAttachment(
      id: id,
      filename: dest_filename,
      original_path: "clipboard",
      mime_type: "image/png",
      size: size,
      created_at: now_iso(),
    )

  // Update index
  update_index(issue_id, attachment)
  |> result.map(fn(_) { attachment })
}

/// Remove an attachment
pub fn remove(
  issue_id: String,
  attachment_id: String,
) -> Result(ImageAttachment, ImageError) {
  case read_index() {
    Ok(index) -> {
      let issue_attachments = dict.get(index, issue_id) |> result.unwrap([])

      case list.find(issue_attachments, fn(a) { a.id == attachment_id }) {
        Ok(attachment) -> {
          // Remove file
          let file_path = images_dir <> "/" <> issue_id <> "/" <> attachment.filename
          simplifile.delete(file_path)
          |> result.unwrap(Nil)

          // Update index
          let new_attachments =
            list.filter(issue_attachments, fn(a) { a.id != attachment_id })
          let new_index = case list.is_empty(new_attachments) {
            True -> dict.delete(index, issue_id)
            False -> dict.insert(index, issue_id, new_attachments)
          }

          case write_index(new_index) {
            Ok(_) -> {
              // Clean up empty directory
              case list.is_empty(new_attachments) {
                True -> simplifile.delete(images_dir <> "/" <> issue_id) |> result.unwrap(Nil)
                False -> Nil
              }
              Ok(attachment)
            }
            Error(e) -> Error(e)
          }
        }
        Error(_) -> Error(StorageError("Attachment not found: " <> attachment_id))
      }
    }
    Error(e) -> Error(e)
  }
}

/// Get full path to an attachment file
pub fn get_path(issue_id: String, attachment_id: String) -> Result(String, ImageError) {
  case read_index() {
    Ok(index) -> {
      let issue_attachments = dict.get(index, issue_id) |> result.unwrap([])
      case list.find(issue_attachments, fn(a) { a.id == attachment_id }) {
        Ok(attachment) ->
          Ok(images_dir <> "/" <> issue_id <> "/" <> attachment.filename)
        Error(_) -> Error(StorageError("Attachment not found"))
      }
    }
    Error(e) -> Error(e)
  }
}

/// Open attachment in default viewer
pub fn open_in_viewer(issue_id: String, attachment_id: String) -> Result(Nil, ImageError) {
  case get_path(issue_id, attachment_id) {
    Ok(path) -> {
      let open_cmd = detect_open_command()
      case shell.run(open_cmd, [path], ".") {
        Ok(_) -> Ok(Nil)
        Error(_) -> Error(StorageError("Failed to open image"))
      }
    }
    Error(e) -> Error(e)
  }
}

/// Get count of attachments for an issue
pub fn count(issue_id: String) -> Int {
  case read_index() {
    Ok(index) -> dict.get(index, issue_id) |> result.unwrap([]) |> list.length
    Error(_) -> 0
  }
}

/// Check if clipboard tools are available
pub fn has_clipboard_support() -> Bool {
  option.is_some(detect_clipboard_tool())
}

/// Build markdown link for bead notes
pub fn build_notes_link(issue_id: String, attachment: ImageAttachment) -> String {
  let relative_path =
    ".beads/images/" <> issue_id <> "/" <> attachment.filename
  let source = case attachment.original_path {
    "clipboard" -> "clipboard"
    _ -> "file"
  }
  "📎 [" <> attachment.filename <> "](" <> relative_path <> ") (" <> source <> ", " <> attachment.created_at <> ")"
}

// ============================================================================
// Internal Functions
// ============================================================================

fn read_index() -> Result(AttachmentIndex, ImageError) {
  let index_path = images_dir <> "/" <> index_file

  // Ensure directory exists
  case simplifile.create_directory_all(images_dir) {
    Ok(_) -> Nil
    Error(_) -> Nil
  }

  case simplifile.read(index_path) {
    Ok(content) -> parse_index(content)
    Error(_) -> Ok(dict.new())
    // File doesn't exist yet
  }
}

fn write_index(index: AttachmentIndex) -> Result(Nil, ImageError) {
  let index_path = images_dir <> "/" <> index_file
  let content = encode_index(index)

  case simplifile.write(index_path, content) {
    Ok(_) -> Ok(Nil)
    Error(_) -> Error(StorageError("Failed to write index"))
  }
}

fn update_index(
  issue_id: String,
  attachment: ImageAttachment,
) -> Result(Nil, ImageError) {
  case read_index() {
    Ok(index) -> {
      let issue_attachments = dict.get(index, issue_id) |> result.unwrap([])
      let new_attachments = list.append(issue_attachments, [attachment])
      let new_index = dict.insert(index, issue_id, new_attachments)
      write_index(new_index)
    }
    Error(e) -> Error(e)
  }
}

fn parse_index(content: String) -> Result(AttachmentIndex, ImageError) {
  case json.parse(content, index_decoder()) {
    Ok(index) -> Ok(index)
    Error(_) -> Ok(dict.new())
  }
}

/// Decoder for the attachment index (Dict of issue_id -> List(ImageAttachment))
fn index_decoder() -> Decoder(AttachmentIndex) {
  decode.dict(decode.string, decode.list(attachment_decoder()))
}

/// Decoder for ImageAttachment using clean use syntax
fn attachment_decoder() -> Decoder(ImageAttachment) {
  use id <- decode.field("id", decode.string)
  use filename <- decode.field("filename", decode.string)
  use original_path <- decode.field("originalPath", decode.string)
  use mime_type <- decode.field("mimeType", decode.string)
  use size <- decode.field("size", decode.int)
  use created_at <- decode.field("createdAt", decode.string)
  decode.success(ImageAttachment(id:, filename:, original_path:, mime_type:, size:, created_at:))
}

/// Encode the attachment index to JSON string
fn encode_index(index: AttachmentIndex) -> String {
  dict.to_list(index)
  |> list.map(fn(pair) {
    let #(id, attachments) = pair
    #(id, json.array(attachments, encode_attachment))
  })
  |> json.object
  |> json.to_string
}

/// Encode a single attachment to JSON
fn encode_attachment(a: ImageAttachment) -> json.Json {
  json.object([
    #("id", json.string(a.id)),
    #("filename", json.string(a.filename)),
    #("originalPath", json.string(a.original_path)),
    #("mimeType", json.string(a.mime_type)),
    #("size", json.int(a.size)),
    #("createdAt", json.string(a.created_at)),
  ])
}

fn detect_clipboard_tool() -> Option(String) {
  // macOS
  case shell.command_exists("pbpaste") {
    True -> Some("pbpaste")
    False -> {
      // Wayland
      case shell.command_exists("wl-paste") {
        True -> Some("wl-paste")
        False -> {
          // X11
          case shell.command_exists("xclip") {
            True -> Some("xclip")
            False -> None
          }
        }
      }
    }
  }
}

fn build_clipboard_command(tool: String, dest_path: String) -> String {
  case tool {
    "pbpaste" ->
      // macOS uses osascript to extract PNG data
      "osascript -e 'set png_data to (the clipboard as «class PNGf»)' "
      <> "-e 'set fp to open for access POSIX file \""
      <> dest_path
      <> "\" with write permission' "
      <> "-e 'write png_data to fp' "
      <> "-e 'close access fp'"
    "wl-paste" -> "wl-paste --type image/png > \"" <> dest_path <> "\""
    "xclip" ->
      "xclip -selection clipboard -t image/png -o > \"" <> dest_path <> "\""
    _ -> "false"
  }
}

fn detect_open_command() -> String {
  case shell.get_env("XDG_CURRENT_DESKTOP") {
    Ok(_) -> "xdg-open"
    Error(_) -> {
      // Probably macOS
      case shell.command_exists("open") {
        True -> "open"
        False -> "xdg-open"
      }
    }
  }
}

fn is_image_file(filename: String) -> Bool {
  let ext = get_extension(filename) |> string.lowercase
  list.contains(image_extensions, ext)
}

fn get_extension(filename: String) -> String {
  case string.split(filename, ".") {
    [_] -> ""
    parts -> "." <> list.last(parts) |> result.unwrap("")
  }
}

fn get_basename(path: String) -> String {
  case string.split(path, "/") {
    [] -> path
    parts -> list.last(parts) |> result.unwrap(path)
  }
}

fn get_mime_type(ext: String) -> String {
  case string.lowercase(ext) {
    ".png" -> "image/png"
    ".jpg" | ".jpeg" -> "image/jpeg"
    ".gif" -> "image/gif"
    ".webp" -> "image/webp"
    ".bmp" -> "image/bmp"
    ".svg" -> "image/svg+xml"
    _ -> "application/octet-stream"
  }
}

fn generate_id() -> String {
  let timestamp = int.to_string(int.absolute_value(unique_integer()))
  let random = int.to_string(int.absolute_value(unique_integer()))
  timestamp <> "-" <> random
}

fn now_iso() -> String {
  tempo.format_utc(tempo.ISO8601Seconds)
}

// ============================================================================
// Guard Functions (for early returns using `use`)
// ============================================================================

/// Guard: file must exist
fn guard_file_exists(
  path: String,
  next: fn(Nil) -> Result(ImageAttachment, ImageError),
) -> Result(ImageAttachment, ImageError) {
  case simplifile.is_file(path) {
    Ok(True) -> next(Nil)
    _ -> Error(FileNotFound(path))
  }
}

/// Guard: file must be a supported image format
fn guard_is_image(
  filename: String,
  next: fn(Nil) -> Result(ImageAttachment, ImageError),
) -> Result(ImageAttachment, ImageError) {
  case is_image_file(filename) {
    True -> next(Nil)
    False ->
      Error(StorageError(
        "Not a supported image format: "
        <> filename
        <> ". Supported: "
        <> string.join(image_extensions, ", "),
      ))
  }
}

/// Guard: directory creation must succeed
fn guard_mkdir(
  dir: String,
  next: fn(Nil) -> Result(ImageAttachment, ImageError),
) -> Result(ImageAttachment, ImageError) {
  case simplifile.create_directory_all(dir) {
    Ok(_) -> next(Nil)
    Error(_) -> Error(StorageError("Failed to create directory: " <> dir))
  }
}

/// Guard: file copy must succeed
fn guard_copy(
  src: String,
  dest: String,
  next: fn(Nil) -> Result(ImageAttachment, ImageError),
) -> Result(ImageAttachment, ImageError) {
  case simplifile.copy_file(src, dest) {
    Ok(_) -> next(Nil)
    Error(_) -> Error(StorageError("Failed to copy file"))
  }
}

/// Guard: clipboard tool must be available
fn guard_clipboard_tool(
  next: fn(String) -> Result(ImageAttachment, ImageError),
) -> Result(ImageAttachment, ImageError) {
  case detect_clipboard_tool() {
    Some(tool) -> next(tool)
    None ->
      Error(ClipboardError(
        "No clipboard tool available. Install xclip (X11) or wl-clipboard (Wayland).",
        None,
      ))
  }
}

/// Guard: clipboard paste command must succeed
fn guard_clipboard_paste(
  cmd: String,
  tool: String,
  next: fn(Nil) -> Result(ImageAttachment, ImageError),
) -> Result(ImageAttachment, ImageError) {
  case shell.run("sh", ["-c", cmd], ".") {
    Ok(_) -> next(Nil)
    Error(_) -> Error(ClipboardError("Failed to get image from clipboard", Some(tool)))
  }
}

/// Guard: clipboard file must exist after paste
fn guard_clipboard_file(
  path: String,
  tool: String,
  next: fn(Nil) -> Result(ImageAttachment, ImageError),
) -> Result(ImageAttachment, ImageError) {
  case simplifile.is_file(path) {
    Ok(True) -> next(Nil)
    _ -> Error(ClipboardError("No image data in clipboard", Some(tool)))
  }
}

/// Guard: clipboard file must have content
fn guard_clipboard_size(
  size: Int,
  path: String,
  tool: String,
  next: fn(Nil) -> Result(ImageAttachment, ImageError),
) -> Result(ImageAttachment, ImageError) {
  case size {
    0 -> {
      let _ = simplifile.delete(path)
      Error(ClipboardError("Clipboard does not contain image data", Some(tool)))
    }
    _ -> next(Nil)
  }
}
