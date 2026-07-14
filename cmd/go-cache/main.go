// Command go-cache provides explicit inventory and maintenance for the
// repository's Go build-cache protocol. It is intentionally developer-facing;
// ordinary shells and validation use the same internal protocol automatically.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/riordanpawley/azedarach/internal/gocache"
)

type inventoryItem struct {
	Path   string        `json:"path"`
	Kind   string        `json:"kind"`
	Exists bool          `json:"exists"`
	Stats  gocache.Stats `json:"stats"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "go-cache:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: go-cache <run|inventory|maintain|cleanup-owner|cleanup-legacy>")
	}
	if args[0] == "run" {
		return runManaged(context.Background(), args[1:])
	}
	cfg, err := gocache.FromEnvironment(gocache.KindNormal)
	if err != nil {
		return err
	}
	switch args[0] {
	case "inventory":
		return printInventory(cfg)
	case "maintain":
		return maintain(context.Background(), cfg)
	case "cleanup-owner":
		flags := flag.NewFlagSet("cleanup-owner", flag.ContinueOnError)
		issue := flags.String("issue", "", "inactive issue owner to clean")
		repo := flags.String("repo", ".", "repository root used for live-owner verification")
		confirm := flags.Bool("confirm", false, "confirm cleanup")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if !*confirm || strings.TrimSpace(*issue) == "" {
			return errors.New("cleanup-owner requires --issue <id> --confirm")
		}
		return gocache.CleanupOwner(context.Background(), cfg.Root, *repo, *issue)
	case "cleanup-legacy":
		flags := flag.NewFlagSet("cleanup-legacy", flag.ContinueOnError)
		confirm := flags.Bool("confirm", false, "confirm supported cleanup of inventoried legacy caches")
		includeGopath := flags.Bool("include-gopath-modcache", false, "also run go clean -modcache in legacy .gopath; never removes bin or other user files")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if !*confirm {
			return errors.New("cleanup-legacy requires --confirm after reviewing inventory")
		}
		return cleanupLegacy(context.Background(), cfg, *includeGopath)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runManaged(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	kindValue := flags.String("kind", string(gocache.KindNormal), "cache kind: normal, race, or coverage")
	if err := flags.Parse(args); err != nil {
		return err
	}
	command := flags.Args()
	if len(command) == 0 {
		return errors.New("run requires -- <command> [arguments]")
	}
	kind := gocache.Kind(*kindValue)
	cfg, err := gocache.FromEnvironment(kind)
	if err != nil {
		return err
	}
	return gocache.WithExclusiveLock(ctx, cfg, func() error {
		telemetry, err := gocache.Prepare(ctx, cfg, os.Getenv("AZEDARACH_GO_CACHE_AUTO_MAINTAIN") == "1")
		if err != nil {
			emitTelemetry(telemetry)
			return err
		}
		cmd := exec.CommandContext(ctx, command[0], command[1:]...)
		cmd.Env = replaceEnvironment(os.Environ(), "GOCACHE", cfg.CachePath())
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		runErr := cmd.Run()
		telemetry, finishErr := gocache.Finish(cfg, telemetry)
		marshalErr := emitTelemetry(telemetry)
		if finishErr == nil && telemetry.FamilyBytes > cfg.HardLimitBytes {
			finishErr = fmt.Errorf("Go build-cache family grew to %d bytes, above hard limit %d", telemetry.FamilyBytes, cfg.HardLimitBytes)
		}
		return errors.Join(runErr, finishErr, marshalErr)
	})
}

func emitTelemetry(telemetry gocache.Telemetry) error {
	encoded, err := json.Marshal(telemetry)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(os.Stderr, "go_cache_telemetry=%s\n", encoded)
	return err
}

func printInventory(cfg gocache.Config) error {
	paths := append([]string{cfg.LayoutRoot()}, gocache.LegacyPaths(cfg.Root)...)
	items := make([]inventoryItem, 0, len(paths))
	for index, path := range paths {
		_, err := os.Stat(path)
		exists := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		stats, err := gocache.StatsFor(path)
		if err != nil {
			return err
		}
		kind := "managed"
		if index > 0 {
			kind = "legacy"
		}
		items = append(items, inventoryItem{Path: path, Kind: kind, Exists: exists, Stats: stats})
	}
	data, err := json.MarshalIndent(map[string]any{"schema": "azedarach.go_cache_inventory.v1", "items": items}, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func maintain(ctx context.Context, cfg gocache.Config) error {
	return gocache.WithExclusiveLock(ctx, cfg, func() error {
		stats, err := gocache.StatsFor(cfg.LayoutRoot())
		if err != nil {
			return err
		}
		if stats.Bytes <= cfg.SoftLimitBytes {
			fmt.Printf("decision=within-soft-limit family_bytes=%d soft_limit_bytes=%d\n", stats.Bytes, cfg.SoftLimitBytes)
			return nil
		}
		// Explicit maintenance cleans the caller's selected namespace through Go's
		// supported operation. Lifecycle cleanup removes inactive issue namespaces.
		if err := gocache.CleanPath(ctx, cfg.CachePath()); err != nil {
			return err
		}
		after, err := gocache.StatsFor(cfg.LayoutRoot())
		if err != nil {
			return err
		}
		fmt.Printf("decision=cleaned-selected-namespace family_bytes_before=%d family_bytes_after=%d\n", stats.Bytes, after.Bytes)
		if after.Bytes > cfg.HardLimitBytes {
			return fmt.Errorf("family remains above hard limit (%d > %d); clean inactive owners explicitly", after.Bytes, cfg.HardLimitBytes)
		}
		return nil
	})
}

func cleanupLegacy(ctx context.Context, cfg gocache.Config, includeGopath bool) error {
	return gocache.WithExclusiveLock(ctx, cfg, func() error {
		legacyBuild := filepath.Join(cfg.Root, "build-cache")
		legacyPaths := gocache.LegacyPaths(cfg.Root)
		legacyDotCache := legacyPaths[1]
		for _, path := range []string{legacyBuild, legacyDotCache} {
			if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err := gocache.CleanPath(ctx, path); err != nil {
				return err
			}
		}
		if includeGopath {
			legacyGopath := legacyPaths[2]
			if _, err := os.Stat(legacyGopath); err == nil {
				cmd := exec.CommandContext(ctx, "go", "clean", "-modcache")
				cmd.Env = replaceEnvironment(os.Environ(), "GOPATH", legacyGopath)
				if output, err := cmd.CombinedOutput(); err != nil {
					return fmt.Errorf("clean legacy module cache: %w: %s", err, output)
				}
			}
		}
		return nil
	})
}

func replaceEnvironment(environment []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		if !strings.HasPrefix(item, prefix) {
			out = append(out, item)
		}
	}
	return append(out, prefix+value)
}
