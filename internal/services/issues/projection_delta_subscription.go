package issues

import "fmt"

type projectionDeltaSubscription struct {
	events chan struct{}
	errors chan error
}

func (c *Client) subscribeProjectionDeltaNotifier() (*projectionDeltaSubscription, func(), error) {
	c.projectionNotifierMu.Lock()
	defer c.projectionNotifierMu.Unlock()

	if c.projectionNotifier == nil {
		notifier, err := newProjectionDeltaNotifier(c.dbPath)
		if err != nil {
			return nil, nil, err
		}
		c.projectionNotifier = notifier
		c.projectionNotifierSubscriptions = make(map[*projectionDeltaSubscription]struct{})
		c.projectionNotifierWG.Add(1)
		go c.runProjectionDeltaNotifier(notifier)
	}

	subscription := &projectionDeltaSubscription{
		events: make(chan struct{}, 1),
		errors: make(chan error, 1),
	}
	c.projectionNotifierSubscriptions[subscription] = struct{}{}
	unsubscribe := func() {
		c.projectionNotifierMu.Lock()
		delete(c.projectionNotifierSubscriptions, subscription)
		c.projectionNotifierMu.Unlock()
	}
	return subscription, unsubscribe, nil
}

func (c *Client) runProjectionDeltaNotifier(notifier projectionDeltaNotifier) {
	defer c.projectionNotifierWG.Done()
	defer func() {
		c.projectionNotifierMu.Lock()
		if c.projectionNotifier == notifier {
			c.projectionNotifier = nil
		}
		for subscription := range c.projectionNotifierSubscriptions {
			close(subscription.events)
			close(subscription.errors)
		}
		c.projectionNotifierSubscriptions = nil
		c.projectionNotifierMu.Unlock()
	}()

	for {
		select {
		case _, ok := <-notifier.Events():
			if !ok {
				return
			}
			c.broadcastProjectionDeltaNotification(nil)
		case err, ok := <-notifier.Errors():
			if !ok {
				return
			}
			if err == nil {
				err = fmt.Errorf("projection notifier failed without an error")
			}
			c.broadcastProjectionDeltaNotification(err)
		}
	}
}

func (c *Client) broadcastProjectionDeltaNotification(notifierErr error) {
	c.projectionNotifierMu.Lock()
	defer c.projectionNotifierMu.Unlock()
	for subscription := range c.projectionNotifierSubscriptions {
		if notifierErr != nil {
			select {
			case subscription.errors <- notifierErr:
			default:
			}
			continue
		}
		select {
		case subscription.events <- struct{}{}:
		default:
		}
	}
}

func (c *Client) closeProjectionDeltaNotifier() error {
	c.projectionNotifierMu.Lock()
	notifier := c.projectionNotifier
	c.projectionNotifierMu.Unlock()
	if notifier == nil {
		return nil
	}
	err := notifier.Close()
	c.projectionNotifierWG.Wait()
	return err
}
