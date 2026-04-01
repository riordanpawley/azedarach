package app

import "time"

const (
	refreshSuccessInterval = 2 * time.Second
	refreshFailureInterval = 5 * time.Second
)

func refreshInterval(lastRefreshSucceeded bool) time.Duration {
	if lastRefreshSucceeded {
		return refreshSuccessInterval
	}
	return refreshFailureInterval
}

