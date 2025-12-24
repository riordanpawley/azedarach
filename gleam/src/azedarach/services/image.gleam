// Image Attachment Service - manages images separately from beads
//
// Since beads CLI doesn't support file attachments, images are stored in:
// - .beads/images/{issue-id}/*.png (actual files)
// - .beads/images/index.json (metadata mapping)
//
// When attaching, we also update the bead's notes with a markdown link.

import gleam/dict.{type Dict}
import gleam/dynamic.{type Dynamic}
import gleam/int
import gleam/json
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/result
import gleam/string
import azedarach/config.{type Config}
import azedarach/util/shell
import simplifile

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
  case simplifile.is_file(file_path) {
    Ok(True) -> Nil
    _ -> return Error(FileNotFound(file_path))
  }

  // Check if it's an image
  let filename = get_basename(file_path)
  case is_image_file(filename) {
    False ->
      return Error(StorageError(
        "Not a supported image format: "
        <> filename
        <> ". Supported: "
        <> string.join(image_extensions, ", "),
      ))
    True -> Nil
  }

  // Create issue directory
  let issue_dir = images_dir <> "/" <> issue_id
  case simplifile.create_directory_all(issue_dir) {
    Ok(_) -> Nil
    Error(_) -> return Error(StorageError("Failed to create directory: " <> issue_dir))
  }

  // Generate unique ID and destination
  let id = generate_id()
  let ext = get_extension(filename)
  let dest_filename = id <> ext
  let dest_path = issue_dir <> "/" <> dest_filename

  // Copy file
  case simplifile.copy_file(file_path, dest_path) {
    Ok(_) -> Nil
    Error(_) -> return Error(StorageError("Failed to copy file"))
  }

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
  case update_index(issue_id, attachment) {
    Ok(_) -> Ok(attachment)
    Error(e) -> Error(e)
  }
}

/// Attach image from clipboard
pub fn attach_from_clipboard(issue_id: String) -> Result(ImageAttachment, ImageError) {
  // Detect clipboard tool
  let tool = detect_clipboard_tool()

  case tool {
    None ->
      return Error(ClipboardError(
        "No clipboard tool available. Install xclip (X11) or wl-clipboard (Wayland).",
        None,
      ))
    Some(t) -> {
      // Create issue directory
      let issue_dir = images_dir <> "/" <> issue_id
      case simplifile.create_directory_all(issue_dir) {
        Ok(_) -> Nil
        Error(_) -> return Error(StorageError("Failed to create directory"))
      }

      // Generate unique ID
      let id = generate_id()
      let dest_filename = id <> ".png"
      let dest_path = issue_dir <> "/" <> dest_filename

      // Build clipboard command
      let cmd = build_clipboard_command(t, dest_path)

      // Execute
      case shell.run("sh", ["-c", cmd], ".") {
        Ok(_) -> Nil
        Error(_) ->
          return Error(ClipboardError("Failed to get image from clipboard", Some(t)))
      }

      // Verify file was created
      case simplifile.is_file(dest_path) {
        Ok(True) -> Nil
        _ -> return Error(ClipboardError("No image data in clipboard", Some(t)))
      }

      // Get file size and verify not empty
      let size = case simplifile.file_info(dest_path) {
        Ok(info) -> info.size
        Error(_) -> 0
      }

      case size {
        0 -> {
          simplifile.delete(dest_path)
          return Error(ClipboardError("Clipboard does not contain image data", Some(t)))
        }
        _ -> Nil
      }

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
      case update_index(issue_id, attachment) {
        Ok(_) -> Ok(attachment)
        Error(e) -> Error(e)
      }
    }
  }
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
  case json.decode(content, index_decoder()) {
    Ok(index) -> Ok(index)
    Error(_) -> Ok(dict.new())
    // Return empty on parse error
  }
}

fn index_decoder() -> fn(Dynamic) ->
  Result(AttachmentIndex, List(dynamic.DecodeError)) {
  dynamic.dict(dynamic.string, dynamic.list(attachment_decoder()))
}

fn attachment_decoder() -> fn(Dynamic) ->
  Result(ImageAttachment, List(dynamic.DecodeError)) {
  dynamic.decode6(
    ImageAttachment,
    dynamic.field("id", dynamic.string),
    dynamic.field("filename", dynamic.string),
    dynamic.field("originalPath", dynamic.string),
    dynamic.field("mimeType", dynamic.string),
    dynamic.field("size", dynamic.int),
    dynamic.field("createdAt", dynamic.string),
  )
}

fn encode_index(index: AttachmentIndex) -> String {
  // Simple JSON encoding
  let entries =
    dict.to_list(index)
    |> list.map(fn(pair) {
      let #(id, attachments) = pair
      "\"" <> id <> "\": [" <> string.join(list.map(attachments, encode_attachment), ", ") <> "]"
    })
  "{" <> string.join(entries, ", ") <> "}"
}

fn encode_attachment(a: ImageAttachment) -> String {
  "{"
  <> "\"id\": \"" <> a.id <> "\", "
  <> "\"filename\": \"" <> a.filename <> "\", "
  <> "\"originalPath\": \"" <> a.original_path <> "\", "
  <> "\"mimeType\": \"" <> a.mime_type <> "\", "
  <> "\"size\": " <> int.to_string(a.size) <> ", "
  <> "\"createdAt\": \"" <> a.created_at <> "\""
  <> "}"
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
  let timestamp = int.to_string(erlang.unique_integer([positive]))
  let random = int.to_string(erlang.unique_integer([positive]))
  timestamp <> "-" <> random
}

fn now_iso() -> String {
  // Simplified - real impl would use datetime library
  "2025-01-01T00:00:00Z"
}

import gleam/erlang
