package issues

import (
	"fmt"

	"github.com/riordanpawley/azedarach/internal/domain"
)

type projectionDeltaSubscription struct {
	events chan struct{}
	errors chan error
}

func (c *Client) subscribeProjectionDeltaNotifier(expectedGeneration uint64) (*projectionDeltaSubscription, func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db == nil {
		return nil, nil, fmt.Errorf("projection delta store is not open: %w", domain.ErrProjectionRetryable)
	}
	if expectedGeneration != c.dbGeneration {
		return nil, nil, fmt.Errorf("projection delta store generation changed from %d to %d: %w", expectedGeneration, c.dbGeneration, domain.ErrProjectionRetryable)
	}

	c.projectionNotifierMu.Lock()
	defer c.projectionNotifierMu.Unlock()

	if c.projectionNotifier == nil {
		notifier, err := newProjectionDeltaNotifier(c.dbPath)
		if err != nil {
			return nil, nil, err
		}
		c.projectionNotifier = notifier
		c.projectionNotifierClose = &projectionDeltaNotifierCloseState{}
		c.projectionNotifierSubscriptions = make(map[*projectionDeltaSubscription]struct{})
		c.projectionNotifierWG.Add(1)
		go c.runProjectionDeltaNotifier(notifier, c.projectionNotifierClose)
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

func (c *Client) runProjectionDeltaNotifier(notifier projectionDeltaNotifier, closeState *projectionDeltaNotifierCloseState) {
	defer c.projectionNotifierWG.Done()
	defer func() {
		_ = closeProjectionDeltaNotifierInstance(notifier, closeState)
		cleared := false
		c.projectionNotifierMu.Lock()
		if c.projectionNotifier == notifier {
			c.projectionNotifier = nil
			c.projectionNotifierClose = nil
			for subscription := range c.projectionNotifierSubscriptions {
				close(subscription.events)
				close(subscription.errors)
			}
			c.projectionNotifierSubscriptions = nil
			cleared = true
		}
		c.projectionNotifierMu.Unlock()
		if cleared && c.projectionNotifierAfterClearHook != nil {
			c.projectionNotifierAfterClearHook()
		}
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
	closeState := c.projectionNotifierClose
	if notifier != nil && closeState == nil {
		closeState = &projectionDeltaNotifierCloseState{}
		c.projectionNotifierClose = closeState
	}
	c.projectionNotifierMu.Unlock()
	var err error
	if notifier != nil {
		err = closeProjectionDeltaNotifierInstance(notifier, closeState)
	}
	// A spontaneously terminated owner clears projectionNotifier before its
	// deferred Done runs. Always join the owner generation, including when the
	// pointer snapshot is nil, so CloseDB cannot reopen across that gap.
	c.projectionNotifierWG.Wait()
	return err
}

func closeProjectionDeltaNotifierInstance(notifier projectionDeltaNotifier, closeState *projectionDeltaNotifierCloseState) error {
	if notifier == nil {
		return nil
	}
	if closeState == nil {
		closeState = &projectionDeltaNotifierCloseState{}
	}
	closeState.once.Do(func() { closeState.err = notifier.Close() })
	return closeState.err
}
