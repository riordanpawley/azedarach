package attachment

import (
	"context"
	"fmt"
	"os/exec"
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
	// Try pngpaste first (faster and more reliable for images)
	if hasPNGPaste() {
		cmd := exec.CommandContext(ctx, "pngpaste", "-")
		output, err := cmd.Output()
		if err == nil && len(output) > 0 {
			return output, nil
		}
	}

	// Fallback to osascript for PNG
	script := `
		set theFile to (path to temporary items folder as text) & "clipboard.png"
		try
			set theImage to the clipboard as «class PNGf»
			set theFileRef to open for access file theFile with write permission
			write theImage to theFileRef
			close access theFileRef
			return POSIX path of theFile
		on error
			return ""
		end try
	`

	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to read clipboard with osascript: %w", err)
	}

	tmpFile := strings.TrimSpace(string(output))
	if tmpFile == "" {
		return nil, fmt.Errorf("no image found in clipboard")
	}

	// Read the temporary file
	cmd = exec.CommandContext(ctx, "cat", tmpFile)
	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to read temporary file: %w", err)
	}

	// Clean up
	exec.CommandContext(ctx, "rm", tmpFile).Run()

	return data, nil
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
	return hasCommand("pngpaste")
}
