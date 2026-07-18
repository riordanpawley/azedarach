package issues

import (
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
)

type fsnotifyProjectionDeltaNotifier struct {
	dbPath  string
	watcher *fsnotify.Watcher
	events  chan struct{}
	errors  chan error
	done    chan struct{}
	once    sync.Once
	wg      sync.WaitGroup
}

func newProjectionDeltaNotifier(dbPath string) (projectionDeltaNotifier, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := watcher.Add(filepath.Dir(dbPath)); err != nil {
		_ = watcher.Close()
		return nil, err
	}
	n := &fsnotifyProjectionDeltaNotifier{
		dbPath:  dbPath,
		watcher: watcher,
		events:  make(chan struct{}, 1),
		errors:  make(chan error, 1),
		done:    make(chan struct{}),
	}
	n.wg.Add(1)
	go n.run()
	return n, nil
}

func (n *fsnotifyProjectionDeltaNotifier) Events() <-chan struct{} { return n.events }
func (n *fsnotifyProjectionDeltaNotifier) Errors() <-chan error    { return n.errors }

func (n *fsnotifyProjectionDeltaNotifier) Close() error {
	var err error
	n.once.Do(func() {
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
	for {
		select {
		case <-n.done:
			return
		case event, ok := <-n.watcher.Events:
			if !ok {
				return
			}
			if !projectionDBEvent(n.dbPath, event.Name) {
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

func projectionDBEvent(dbPath, eventPath string) bool {
	dbPath, eventPath = filepath.Clean(dbPath), filepath.Clean(eventPath)
	return eventPath == dbPath || eventPath == dbPath+"-wal" || eventPath == dbPath+"-shm"
}
