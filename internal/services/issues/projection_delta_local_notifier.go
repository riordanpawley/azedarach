package issues

import "sync"

var localProjectionDeltaNotifications = struct {
	sync.Mutex
	subscribers map[string]map[chan struct{}]struct{}
}{subscribers: make(map[string]map[chan struct{}]struct{})}

// registerLocalProjectionDeltaNotification supplements the descriptor-safe
// filesystem notifier for clients sharing a process. Filesystem events may be
// coalesced, while a committed local writer can fan out the wake directly.
func registerLocalProjectionDeltaNotification(dbPath string, events chan struct{}) func() {
	path := projectionDeltaNotificationPath(dbPath)
	localProjectionDeltaNotifications.Lock()
	if localProjectionDeltaNotifications.subscribers[path] == nil {
		localProjectionDeltaNotifications.subscribers[path] = make(map[chan struct{}]struct{})
	}
	localProjectionDeltaNotifications.subscribers[path][events] = struct{}{}
	localProjectionDeltaNotifications.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			localProjectionDeltaNotifications.Lock()
			delete(localProjectionDeltaNotifications.subscribers[path], events)
			if len(localProjectionDeltaNotifications.subscribers[path]) == 0 {
				delete(localProjectionDeltaNotifications.subscribers, path)
			}
			localProjectionDeltaNotifications.Unlock()
		})
	}
}

func signalLocalProjectionDeltaNotification(dbPath string) {
	path := projectionDeltaNotificationPath(dbPath)
	// Keep unregister and channel close ordered behind every in-flight send.
	localProjectionDeltaNotifications.Lock()
	defer localProjectionDeltaNotifications.Unlock()
	for events := range localProjectionDeltaNotifications.subscribers[path] {
		select {
		case events <- struct{}{}:
		default:
		}
	}
}
