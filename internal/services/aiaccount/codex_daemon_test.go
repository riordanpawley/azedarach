package aiaccount

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestClassifyCodexDaemon(t *testing.T) {
	tests := []struct {
		command string
		kind    string
		match   bool
	}{
		{"/opt/bin/codex app-server", "app-server", true},
		{"codex --cd /tmp mcp-server", "mcp-server", true},
		{"codex mcp serve", "mcp-server", true},
		{"codex proto", "proto", true},
		{"codex", "", false},
		{"other app-server", "", false},
	}
	for _, tt := range tests {
		kind, match := classifyCodexDaemon(tt.command)
		if kind != tt.kind || match != tt.match {
			t.Errorf("classifyCodexDaemon(%q) = %q, %v; want %q, %v", tt.command, kind, match, tt.kind, tt.match)
		}
	}
}

func TestSystemCodexDaemonControllerScopesAndFailsSafe(t *testing.T) {
	var signaled []int
	controller := systemCodexDaemonController{
		scan: func(context.Context) ([]codexDaemonProcess, bool, error) {
			return []codexDaemonProcess{
				{pid: 10, subcommand: "app-server", codexHome: "/profiles/work", attributed: true},
				{pid: 20, subcommand: "mcp-server", codexHome: "/profiles/personal", attributed: true},
				{pid: 30, subcommand: "proto", attributed: false},
				{pid: 40, subcommand: "mcp-server", codexHome: "/profiles/work", attributed: true},
			}, true, nil
		},
		signal: func(pid int) error {
			signaled = append(signaled, pid)
			if pid == 40 {
				return errors.New("gone")
			}
			return nil
		},
	}
	result, err := controller.Reload(context.Background(), "/profiles/work", true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(signaled, []int{10, 40}) {
		t.Fatalf("signaled = %v, want only scoped pids", signaled)
	}
	if !reflect.DeepEqual(result.DetectedPIDs, []int{10, 40}) || !reflect.DeepEqual(result.ReloadedPIDs, []int{10}) || !reflect.DeepEqual(result.FailedPIDs, []int{40}) {
		t.Fatalf("result = %+v", result)
	}
	if result.UnattributedCount != 1 {
		t.Fatalf("unattributed count = %d", result.UnattributedCount)
	}
}

func TestSystemCodexDaemonControllerPrefersNativeLifecycle(t *testing.T) {
	scanned := false
	restarted := false
	controller := systemCodexDaemonController{
		nativeRunning: func(context.Context, string) bool { return true },
		nativeRestart: func(_ context.Context, home string) error {
			restarted = home == "/profiles/work"
			return nil
		},
		scan: func(context.Context) ([]codexDaemonProcess, bool, error) {
			scanned = true
			return nil, true, nil
		},
	}
	result, err := controller.Reload(context.Background(), "/profiles/work", true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.NativeDaemon || !result.NativeRestarted || !restarted || scanned {
		t.Fatalf("native result=%+v restarted=%v scanned=%v", result, restarted, scanned)
	}
}

func TestSystemCodexDaemonControllerFallsBackAfterNativeRestartFailure(t *testing.T) {
	var signaled []int
	controller := systemCodexDaemonController{
		nativeRunning: func(context.Context, string) bool { return true },
		nativeRestart: func(context.Context, string) error { return errors.New("restart failed") },
		scan: func(context.Context) ([]codexDaemonProcess, bool, error) {
			return []codexDaemonProcess{{pid: 42, subcommand: "app-server", codexHome: "/profiles/work", attributed: true}}, true, nil
		},
		signal: func(pid int) error { signaled = append(signaled, pid); return nil },
	}
	result, err := controller.Reload(context.Background(), "/profiles/work", true)
	if err != nil {
		t.Fatal(err)
	}
	if result.NativeRestarted || !reflect.DeepEqual(signaled, []int{42}) || !reflect.DeepEqual(result.ReloadedPIDs, []int{42}) {
		t.Fatalf("fallback result=%+v signaled=%v", result, signaled)
	}
}

func TestCodexHomeFromEnvironment(t *testing.T) {
	tests := []struct {
		name       string
		raw        []byte
		readable   bool
		want       string
		attributed bool
	}{
		{"explicit", []byte("HOME=/home/alice\x00CODEX_HOME=/profiles/work\x00TOKEN=secret"), true, "/profiles/work", true},
		{"home fallback", []byte("HOME=/home/alice\x00TOKEN=secret"), true, filepath.Join("/home/alice", ".codex"), true},
		{"unreadable", nil, false, "", false},
		{"missing", []byte("PATH=/bin"), true, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, attributed := codexHomeFromEnvironment(tt.raw, tt.readable)
			if got != tt.want || attributed != tt.attributed {
				t.Fatalf("got %q, %v; want %q, %v", got, attributed, tt.want, tt.attributed)
			}
		})
	}
}

func TestRunCommandBoundedStopsOversizedProducer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := runCommandBounded(ctx, "sh", "-c", fmt.Sprintf("head -c %d /dev/zero", maxProcessInspectionBytes+1024))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v, want bounded-output error", err)
	}
}
