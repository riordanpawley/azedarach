package gocache

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const secureOpenFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW

var cleanupOwnerBeforeRemoveHook func(string)

// openCacheRoot resolves only the already-existing prefix, then creates and
// opens every cache-root component relative to a held directory descriptor.
// Descendants are never resolved through a symlink.
func openCacheRoot(path string, create bool) (*os.File, string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, "", fmt.Errorf("resolve Go cache root: %w", err)
	}
	if strings.TrimSpace(path) == "" || abs == string(filepath.Separator) {
		return nil, "", errors.New("Go cache root must be a non-root directory")
	}

	existing := abs
	var missing []string
	for {
		info, statErr := os.Lstat(existing)
		if statErr == nil {
			if len(missing) == 0 && info.Mode()&os.ModeSymlink != 0 {
				return nil, "", fmt.Errorf("refusing symlink Go cache root %s", abs)
			}
			break
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return nil, "", fmt.Errorf("inspect Go cache root component %s: %w", existing, statErr)
		}
		if !create {
			return nil, "", os.ErrNotExist
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return nil, "", fmt.Errorf("find existing parent for Go cache root %s", abs)
		}
		missing = append(missing, filepath.Base(existing))
		existing = parent
	}

	canonical, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return nil, "", fmt.Errorf("canonicalize Go cache root parent %s: %w", existing, err)
	}
	fd, err := unix.Open(canonical, secureOpenFlags, 0)
	if err != nil {
		return nil, "", fmt.Errorf("open canonical Go cache root parent %s: %w", canonical, err)
	}
	current := os.NewFile(uintptr(fd), canonical)
	if current == nil {
		_ = unix.Close(fd)
		return nil, "", fmt.Errorf("wrap Go cache root descriptor %s", canonical)
	}
	for index := len(missing) - 1; index >= 0; index-- {
		next, openErr := openChildDir(current, missing[index], true)
		if openErr != nil {
			_ = current.Close()
			return nil, "", openErr
		}
		_ = current.Close()
		current = next
		canonical = filepath.Join(canonical, missing[index])
	}
	return current, canonical, nil
}

func openChildDir(parent *os.File, name string, create bool) (*os.File, error) {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return nil, fmt.Errorf("invalid Go cache path component %q", name)
	}
	fd, err := unix.Openat(int(parent.Fd()), name, secureOpenFlags, 0)
	if err != nil && errors.Is(err, unix.ENOENT) && create {
		if mkdirErr := unix.Mkdirat(int(parent.Fd()), name, 0o755); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
			return nil, fmt.Errorf("create Go cache component %s: %w", name, mkdirErr)
		}
		fd, err = unix.Openat(int(parent.Fd()), name, secureOpenFlags, 0)
	}
	if err != nil {
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			return nil, fmt.Errorf("refusing symlink namespace or non-directory Go cache component %s: %w", name, err)
		}
		return nil, fmt.Errorf("open Go cache component %s: %w", name, err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap Go cache component descriptor %s", name)
	}
	return file, nil
}

func openManagedDir(cfg Config, components []string, create bool) (*os.File, error) {
	root, _, err := openCacheRoot(cfg.Root, create)
	if err != nil {
		return nil, err
	}
	current := root
	for _, component := range components {
		next, openErr := openChildDir(current, component, create)
		_ = current.Close()
		if openErr != nil {
			return nil, openErr
		}
		current = next
	}
	return current, nil
}

func managedStats(cfg Config, namespace bool) (Stats, error) {
	components := []string{"caches", LayoutVersion}
	if namespace {
		components = append(components, string(cfg.Kind), cfg.Owner)
	}
	dir, err := openManagedDir(cfg, components, false)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ENOENT) {
		return Stats{}, nil
	}
	if err != nil {
		return Stats{}, err
	}
	defer dir.Close()
	return statsFromDir(dir)
}

func statsFromDir(dir *os.File) (Stats, error) {
	var out Stats
	names, err := dir.Readdirnames(-1)
	if err != nil && !errors.Is(err, io.EOF) {
		return out, err
	}
	for _, name := range names {
		var stat unix.Stat_t
		if err := unix.Fstatat(int(dir.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return out, err
		}
		switch stat.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			child, err := openChildDir(dir, name, false)
			if err != nil {
				return out, err
			}
			childStats, childErr := statsFromDir(child)
			_ = child.Close()
			if childErr != nil {
				return out, childErr
			}
			out.Bytes += childStats.Bytes
			out.Files += childStats.Files
		case unix.S_IFLNK:
			return out, fmt.Errorf("refusing symlink within managed Go cache at %s", name)
		default:
			out.Bytes += stat.Size
			out.Files++
		}
	}
	return out, nil
}

func sameFileIdentity(left, right *unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino && left.Mode&unix.S_IFMT == right.Mode&unix.S_IFMT
}

func removeDirContents(dir *os.File) error {
	names, err := dir.Readdirnames(-1)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	for _, name := range names {
		var stat unix.Stat_t
		if err := unix.Fstatat(int(dir.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return err
		}
		if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
			child, err := openChildDir(dir, name, false)
			if err != nil {
				return err
			}
			if err := removeDirContents(child); err != nil {
				_ = child.Close()
				return err
			}
			var opened unix.Stat_t
			if err := unix.Fstat(int(child.Fd()), &opened); err != nil {
				_ = child.Close()
				return err
			}
			_ = child.Close()
			var current unix.Stat_t
			if err := unix.Fstatat(int(dir.Fd()), name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				if errors.Is(err, unix.ENOENT) {
					continue
				}
				return err
			}
			if !sameFileIdentity(&opened, &current) {
				return fmt.Errorf("Go cache directory changed during cleanup at %s", name)
			}
			if err := unix.Unlinkat(int(dir.Fd()), name, unix.AT_REMOVEDIR); err != nil {
				return err
			}
			continue
		}
		if err := unix.Unlinkat(int(dir.Fd()), name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return err
		}
	}
	return nil
}
