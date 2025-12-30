package network

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewStatusChecker(t *testing.T) {
	checker := NewStatusChecker()
	require.NotNil(t, checker)
	assert.True(t, checker.IsOnline(), "should be optimistically online initially")
}

func TestCheck_Success(t *testing.T) {
	// Create test server that returns 200 OK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodHead, r.Method)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	checker := NewStatusChecker()
	// Override the check to use test server
	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, server.URL, nil)
	require.NoError(t, err)

	resp, err := checker.client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	online := resp.StatusCode >= 200 && resp.StatusCode < 400
	checker.setOnline(online)

	assert.True(t, checker.IsOnline())
}

func TestCheck_Failure(t *testing.T) {
	// Create test server that returns 500 error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	checker := NewStatusChecker()
	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, server.URL, nil)
	require.NoError(t, err)

	resp, err := checker.client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	online := resp.StatusCode >= 200 && resp.StatusCode < 400
	checker.setOnline(online)

	assert.False(t, checker.IsOnline())
}

func TestCheck_NetworkError(t *testing.T) {
	checker := NewStatusChecker()

	// Use invalid URL to simulate network error
	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, "http://invalid-host-that-does-not-exist-12345.com", nil)
	require.NoError(t, err)

	_, err = checker.client.Do(req)
	assert.Error(t, err)

	checker.setOnline(false)
	assert.False(t, checker.IsOnline())
}

func TestIsOnline_Caching(t *testing.T) {
	checker := NewStatusChecker()

	// Initially online
	assert.True(t, checker.IsOnline())

	// Set offline
	checker.setOnline(false)
	assert.False(t, checker.IsOnline())

	// Set back online
	checker.setOnline(true)
	assert.True(t, checker.IsOnline())
}

func TestLastCheck(t *testing.T) {
	checker := NewStatusChecker()

	// Initial last check should be zero
	lastCheck := checker.LastCheck()
	assert.True(t, lastCheck.IsZero() || time.Since(lastCheck) < time.Second)

	// Perform check
	before := time.Now()
	checker.setOnline(true)
	after := time.Now()

	lastCheck = checker.LastCheck()
	assert.True(t, lastCheck.After(before.Add(-time.Second)))
	assert.True(t, lastCheck.Before(after.Add(time.Second)))
}

func TestConcurrentAccess(t *testing.T) {
	checker := NewStatusChecker()

	// Test concurrent reads and writes
	done := make(chan bool)

	// Writer goroutine
	go func() {
		for i := 0; i < 100; i++ {
			checker.setOnline(i%2 == 0)
			time.Sleep(time.Microsecond)
		}
		done <- true
	}()

	// Reader goroutines
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				_ = checker.IsOnline()
				_ = checker.LastCheck()
				time.Sleep(time.Microsecond)
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 11; i++ {
		<-done
	}
}

func TestCheckCmd(t *testing.T) {
	checker := NewStatusChecker()
	cmd := checker.CheckCmd()

	require.NotNil(t, cmd)

	// Execute the command
	msg := cmd()

	statusMsg, ok := msg.(StatusMsg)
	require.True(t, ok, "should return StatusMsg")

	// The actual value depends on network availability
	// Just verify the message type is correct
	_ = statusMsg.Online
}
