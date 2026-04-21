package attachment

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ReadImageFromClipboard reads an image from the system clipboard
// Supports macOS (pbpaste), Linux (wl-paste, xclip)
func ReadImageFromClipboard(ctx context.Context) ([]byte, error) {
	switch runtime.GOOS {
	case "darwin":
		return readClipboardMacOS(ctx)
	case "linux":
		return readClipboardLinux(ctx)
	default:
		return nil, fmt.Errorf("clipboard reading not supported on %s", runtime.GOOS)
	}
}

// readClipboardMacOS reads clipboard on macOS using osascript/pngpaste
func readClipboardMacOS(ctx context.Context) ([]byte, error) {
	var attempts []string

	// Try pngpaste first (faster and more reliable for images)
	if pngpastePath, ok := findPNGPastePath(); ok {
		cmd := exec.CommandContext(ctx, pngpastePath, "-")
		output, err := cmd.Output()
		if err == nil && len(output) > 0 {
			return output, nil
		}
		if err != nil {
			attempts = append(attempts, "pngpaste failed: "+compactWhitespace(err.Error()))
		} else {
			attempts = append(attempts, "pngpaste returned empty output")
		}
	} else {
		attempts = append(attempts, "pngpaste not installed")
	}

	// Preserve compatibility with prior clipboard flows where pbpaste exposes raw payload bytes.
	if data, err := readClipboardMacOSPBPasteRaw(ctx); err == nil && len(data) > 0 {
		return data, nil
	} else if err != nil {
		attempts = append(attempts, "pbpaste raw failed: "+compactWhitespace(err.Error()))
	}

	// Try the direct PNG AppleScript flow used in earlier implementations.
	if data, err := readClipboardMacOSPNGScript(ctx); err == nil && len(data) > 0 {
		return data, nil
	} else if err != nil {
		attempts = append(attempts, "png applescript failed: "+compactWhitespace(err.Error()))
	}

	// Native screenshot clipboard payloads can require direct NSPasteboard access.
	if data, err := readClipboardMacOSPasteboardData(ctx); err == nil && len(data) > 0 {
		return data, nil
	} else if err != nil {
		attempts = append(attempts, "pasteboard fallback failed: "+compactWhitespace(err.Error()))
	}

	// Some apps place a file alias on the clipboard instead of raw image bytes.
	if data, err := readClipboardMacOSFileAlias(ctx); err == nil && len(data) > 0 {
		return data, nil
	} else if err != nil {
		attempts = append(attempts, "file alias fallback failed: "+compactWhitespace(err.Error()))
	}

	// Some clipboard flows expose an image file path as plain text.
	if data, err := readClipboardMacOSTextPath(ctx); err == nil && len(data) > 0 {
		return data, nil
	} else if err != nil {
		attempts = append(attempts, "text path fallback failed: "+compactWhitespace(err.Error()))
	}

	// Fallback to osascript for PNG/TIFF/JPEG.
	script := `
		set tmpDir to POSIX path of (path to temporary items folder)
		try
			set pngData to the clipboard as «class PNGf»
			set pngPath to tmpDir & "clipboard-image.png"
			my writeDataToFile(pngData, pngPath)
			return pngPath
		on error pngErr
			try
				set tiffData to the clipboard as TIFF picture
				set tiffPath to tmpDir & "clipboard-image.tiff"
				my writeDataToFile(tiffData, tiffPath)
				return tiffPath
			on error tiffErr
				try
					set jpegData to the clipboard as JPEG picture
					set jpegPath to tmpDir & "clipboard-image.jpg"
					my writeDataToFile(jpegData, jpegPath)
					return jpegPath
				on error jpegErr
					return "ERROR:" & pngErr & " | " & tiffErr & " | " & jpegErr
				end try
			end try
		end try
		
		on writeDataToFile(theData, outPath)
			set outFile to open for access (POSIX file outPath) with write permission
			set eof outFile to 0
			write theData to outFile
			close access outFile
		end writeDataToFile
	`

	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	output, err := cmd.Output()
	if err != nil {
		attempts = append(attempts, "osascript execution failed: "+compactWhitespace(err.Error()))
		return nil, fmt.Errorf("failed to read clipboard image on macOS (%s)", compactWhitespace(strings.Join(attempts, "; ")))
	}

	result := compactWhitespace(strings.TrimSpace(string(output)))
	if strings.HasPrefix(result, "ERROR:") {
		attempts = append(attempts, strings.TrimSpace(strings.TrimPrefix(result, "ERROR:")))
		if clipboardInfo := readClipboardMacOSInfo(ctx); clipboardInfo != "" {
			attempts = append(attempts, "clipboard info: "+clipboardInfo)
		}
		return nil, fmt.Errorf("no image found in clipboard (%s)", compactWhitespace(strings.Join(attempts, "; ")))
	}
	if result == "" {
		attempts = append(attempts, "osascript returned empty path")
		if clipboardInfo := readClipboardMacOSInfo(ctx); clipboardInfo != "" {
			attempts = append(attempts, "clipboard info: "+clipboardInfo)
		}
		return nil, fmt.Errorf("no image found in clipboard (%s)", compactWhitespace(strings.Join(attempts, "; ")))
	}

	tmpFile := filepath.Clean(result)
	data, err := os.ReadFile(tmpFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read temporary file: %w", err)
	}

	// Clean up
	_ = os.Remove(tmpFile)

	return data, nil
}

func readClipboardMacOSFileAlias(ctx context.Context) ([]byte, error) {
	script := `
		try
			set aliasPath to POSIX path of (the clipboard as alias)
			return aliasPath
		on error errMsg
			return "ERROR:" & errMsg
		end try
	`
	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("osascript alias read failed: %w", err)
	}
	result := strings.TrimSpace(string(output))
	if strings.HasPrefix(result, "ERROR:") {
		return nil, fmt.Errorf(strings.TrimPrefix(result, "ERROR:"))
	}
	if result == "" {
		return nil, fmt.Errorf("clipboard alias path was empty")
	}
	path := filepath.Clean(result)
	if !isLikelyImagePath(path) {
		return nil, fmt.Errorf("clipboard file is not an image: %s", filepath.Ext(path))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read clipboard alias image: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("clipboard alias image file was empty")
	}
	return data, nil
}

func readClipboardMacOSPasteboardData(ctx context.Context) ([]byte, error) {
	script := `
ObjC.import("AppKit");
ObjC.import("Foundation");

function extForType(t) {
	const lower = String(t || "").toLowerCase();
	if (lower.includes("png")) return "png";
	if (lower.includes("tiff")) return "tiff";
	if (lower.includes("jpeg") || lower.includes("jpg")) return "jpg";
	if (lower.includes("heic")) return "heic";
	if (lower.includes("heif")) return "heif";
	if (lower.includes("gif")) return "gif";
	if (lower.includes("webp")) return "webp";
	return "img";
}

function isImageLikeType(t) {
	const lower = String(t || "").toLowerCase();
	return (
		lower.includes("image") ||
		lower.includes("png") ||
		lower.includes("tiff") ||
		lower.includes("jpeg") ||
		lower.includes("jpg") ||
		lower.includes("heic") ||
		lower.includes("heif") ||
		lower.includes("gif") ||
		lower.includes("webp")
	);
}

(function () {
	const pb = $.NSPasteboard.generalPasteboard;
	const typeObjs = pb.types;
	const types = [];
	if (typeObjs) {
		const count = typeObjs.count;
		for (let i = 0; i < count; i++) {
			types.push(ObjC.unwrap(typeObjs.objectAtIndex(i)));
		}
	}

	const preferred = [
		"public.png",
		"public.tiff",
		"public.jpeg",
		"public.heic",
		"public.heif",
		"com.compuserve.gif",
		"org.webmproject.webp"
	];
	const candidates = preferred.slice();
	for (let i = 0; i < types.length; i++) {
		const t = types[i];
		if (isImageLikeType(t) && candidates.indexOf(t) === -1) {
			candidates.push(t);
		}
	}

	const tmpDir = ObjC.unwrap($.NSTemporaryDirectory());
	const pid = ObjC.unwrap($.NSProcessInfo.processInfo.processIdentifier);

	for (let i = 0; i < candidates.length; i++) {
		const t = candidates[i];
		const data = pb.dataForType(t);
		if (!data) continue;
		const filePath = tmpDir + "azedarach-clipboard-" + pid + "." + extForType(t);
		const ok = data.writeToFileAtomically($(filePath), true);
		if (ok) return filePath;
	}

	return "ERROR:no supported image data in pasteboard; types=" + types.join(",");
})();
`

	cmd := exec.CommandContext(ctx, "osascript", "-l", "JavaScript", "-e", script)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("jxa pasteboard read failed: %w: %s", err, compactWhitespace(string(output)))
	}

	result := strings.TrimSpace(string(output))
	if strings.HasPrefix(result, "ERROR:") {
		return nil, fmt.Errorf(strings.TrimPrefix(result, "ERROR:"))
	}
	if result == "" {
		return nil, fmt.Errorf("jxa pasteboard fallback returned empty path")
	}

	path := filepath.Clean(result)
	defer func() {
		_ = os.Remove(path)
	}()
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		return nil, fmt.Errorf("read jxa pasteboard image: %w", readErr)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("jxa pasteboard image file was empty")
	}
	return data, nil
}

func readClipboardMacOSTextPath(ctx context.Context) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "pbpaste")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("pbpaste failed: %w", err)
	}
	raw := strings.TrimSpace(string(output))
	if raw == "" {
		return nil, fmt.Errorf("clipboard text is empty")
	}

	path := raw
	if strings.HasPrefix(raw, "file://") {
		u, parseErr := url.Parse(raw)
		if parseErr != nil {
			return nil, fmt.Errorf("parse file url: %w", parseErr)
		}
		path = u.Path
	}
	path = filepath.Clean(path)
	if !isLikelyImagePath(path) {
		return nil, fmt.Errorf("clipboard text is not image path")
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		return nil, fmt.Errorf("read image path from clipboard text: %w", readErr)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("clipboard text image path file is empty")
	}
	return data, nil
}

func readClipboardMacOSPNGScript(ctx context.Context) ([]byte, error) {
	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("azedarach-clipboard-%d.png", os.Getpid()))
	defer func() {
		_ = os.Remove(tmpFile)
	}()
	quotedTmpPath := applescriptStringLiteral(tmpFile)

	args := []string{
		"-e", "set png_data to (the clipboard as «class PNGf»)",
		"-e", fmt.Sprintf("set fp to open for access POSIX file \"%s\" with write permission", quotedTmpPath),
		"-e", "set eof fp to 0",
		"-e", "write png_data to fp",
		"-e", "close access fp",
	}
	cmd := exec.CommandContext(ctx, "osascript", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, compactWhitespace(string(output)))
	}

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		return nil, fmt.Errorf("read png applescript output: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("png applescript produced empty file")
	}
	return data, nil
}

func readClipboardMacOSPBPasteRaw(ctx context.Context) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "pbpaste")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("pbpaste execution failed: %w", err)
	}
	if len(output) == 0 {
		return nil, fmt.Errorf("pbpaste returned empty output")
	}
	mime := detectMimeType(output)
	if !strings.HasPrefix(mime, "image/") {
		return nil, fmt.Errorf("pbpaste returned non-image mime %s", mime)
	}
	return output, nil
}

// readClipboardLinux reads clipboard on Linux using wl-paste or xclip
func readClipboardLinux(ctx context.Context) ([]byte, error) {
	// Try wl-paste first (Wayland)
	if hasCommand("wl-paste") {
		cmd := exec.CommandContext(ctx, "wl-paste", "--type", "image/png")
		output, err := cmd.Output()
		if err == nil && len(output) > 0 {
			return output, nil
		}

		// Try without specifying type
		cmd = exec.CommandContext(ctx, "wl-paste", "--no-newline")
		output, err = cmd.Output()
		if err == nil && len(output) > 0 && detectMimeType(output) != "application/octet-stream" {
			return output, nil
		}
	}

	// Try xclip (X11)
	if hasCommand("xclip") {
		cmd := exec.CommandContext(ctx, "xclip", "-selection", "clipboard", "-t", "image/png", "-o")
		output, err := cmd.Output()
		if err == nil && len(output) > 0 {
			return output, nil
		}

		// Try JPEG
		cmd = exec.CommandContext(ctx, "xclip", "-selection", "clipboard", "-t", "image/jpeg", "-o")
		output, err = cmd.Output()
		if err == nil && len(output) > 0 {
			return output, nil
		}
	}

	return nil, fmt.Errorf("no clipboard tool found (tried wl-paste, xclip)")
}

// hasCommand checks if a command is available in PATH
func hasCommand(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// hasPNGPaste checks if pngpaste is installed on macOS
func hasPNGPaste() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	_, ok := findPNGPastePath()
	return ok
}

func findPNGPastePath() (string, bool) {
	if runtime.GOOS != "darwin" {
		return "", false
	}
	if path, err := exec.LookPath("pngpaste"); err == nil {
		return path, true
	}
	commonPaths := []string{
		"/opt/homebrew/bin/pngpaste",
		"/usr/local/bin/pngpaste",
	}
	for _, candidate := range commonPaths {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
	}
	return "", false
}

func compactWhitespace(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func readClipboardMacOSInfo(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "osascript", "-e", "clipboard info")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return compactWhitespace(string(output))
}

func isLikelyImagePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tif", ".tiff", ".heic", ".heif":
		return true
	default:
		return false
	}
}

func applescriptStringLiteral(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(escaped, `"`, `\"`)
}
