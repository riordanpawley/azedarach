package issues

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
	"github.com/riordanpawley/azedarach/internal/sqliteutil"
)

type fsnotifyProjectionDeltaNotifier struct {
	signalPath      string
	watcher         *fsnotify.Watcher
	events          chan struct{}
	errors          chan error
	done            chan struct{}
	once            sync.Once
	wg              sync.WaitGroup
	unregisterLocal func()
}

func newProjectionDeltaNotifier(dbPath string) (projectionDeltaNotifier, error) {
	signalPath := projectionDeltaNotificationPath(dbPath)
	signalFile, err := os.OpenFile(signalPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := signalFile.Close(); err != nil {
		return nil, err
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := watcher.Add(signalPath); err != nil {
		_ = watcher.Close()
		return nil, err
	}
	n := &fsnotifyProjectionDeltaNotifier{
		signalPath: signalPath,
		watcher:    watcher,
		events:     make(chan struct{}, 1),
		errors:     make(chan error, 1),
		done:       make(chan struct{}),
	}
	n.unregisterLocal = registerLocalProjectionDeltaNotification(dbPath, n.events)
	n.wg.Add(1)
	go n.run()
	return n, nil
}

func (n *fsnotifyProjectionDeltaNotifier) Events() <-chan struct{} { return n.events }
func (n *fsnotifyProjectionDeltaNotifier) Errors() <-chan error    { return n.errors }

func (n *fsnotifyProjectionDeltaNotifier) Close() error {
	var err error
	n.once.Do(func() {
		n.unregisterLocal()
		close(n.done)
		err = n.watcher.Close()
		n.wg.Wait()
	})
	return err
}

func (n *fsnotifyProjectionDeltaNotifier) run() {
	defer n.wg.Done()
	defer close(n.events)
	defer close(n.errors)
	defer n.unregisterLocal()
	for {
		select {
		case <-n.done:
			return
		case event, ok := <-n.watcher.Events:
			if !ok {
				return
			}
			if filepath.Clean(event.Name) != n.signalPath {
				continue
			}
			select {
			case n.events <- struct{}{}:
			default:
			}
		case err, ok := <-n.watcher.Errors:
			if !ok {
				return
			}
			select {
			case n.errors <- err:
			default:
			}
		}
	}
}

func projectionDeltaNotificationPath(dbPath string) string {
	return sqliteutil.CanonicalPath(dbPath) + ".projection-notify"
}

func signalProjectionDeltaNotification(dbPath string) error {
	// A sidecar failure must not suppress reliable same-process wakeups after
	// the authoritative SQLite transaction has already committed.
	defer signalLocalProjectionDeltaNotification(dbPath)
	signalFile, err := os.OpenFile(projectionDeltaNotificationPath(dbPath), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := signalFile.WriteAt([]byte{0}, 0)
	closeErr := signalFile.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
