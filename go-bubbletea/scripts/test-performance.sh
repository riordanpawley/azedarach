#!/bin/bash
# test-performance.sh - Automated performance testing for az TUI

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Ensure we're in the right directory
cd /Users/riordan/prog/azedarach-az-r883/go-bubbletea

# Build the binary
echo "Building az binary..."
go build ./cmd/az

# Duration of the test in seconds
DURATION=10
INTERVAL=0.1

echo "Starting performance test..."

cat <<'GOEOF' >internal/app/perf_test.go
package app

import (
	"fmt"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/services/monitor"
)

func BenchmarkUpdateLoop(b *testing.B) {
	cfg := config.DefaultConfig()
	m := New(cfg)
	m.width = 100
	m.height = 40
	m.loading = false

	// Mock some tasks
	m.tasks = make([]domain.Task, 100)
	for i := 0; i < 100; i++ {
		m.tasks[i] = domain.Task{
			ID:     fmt.Sprintf("az-%d", i),
			Title:  fmt.Sprintf("Task %d", i),
			Status: domain.StatusOpen,
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Simulate a key press
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
		newM, _ := m.Update(msg)
		m = newM.(Model)
		
		// Simulate a tick message (periodic refresh)
		tickMsg := tickMsg(time.Now())
		newTickM, _ := m.Update(tickMsg)
		m = newTickM.(Model)
		
		// Simulate a session monitor message
		sessionMsg := monitor.SessionStateMsg{
			BeadID: "az-1",
			State:  domain.SessionBusy,
		}
		newSessionM, _ := m.Update(sessionMsg)
		m = newSessionM.(Model)
	}
}

func TestPerformanceRamp(t *testing.T) {
	cfg := config.DefaultConfig()
	m := New(cfg)
	m.width = 100
	m.height = 40
	m.loading = false

	// Mock some tasks
	m.tasks = make([]domain.Task, 100)
	for i := 0; i < 100; i++ {
		m.tasks[i] = domain.Task{
			ID:     fmt.Sprintf("az-%d", i),
			Title:  fmt.Sprintf("Task %d", i),
			Status: domain.StatusOpen,
		}
	}

	duration := 10 * time.Second
	start := time.Now()
	iterations := 0
	
	lastLog := time.Now()
	
	for time.Since(start) < duration {
		iterStart := time.Now()
		
		// Simulate messages that happen in real app
		newKM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = newKM.(Model)
		newTM, _ := m.Update(tickMsg(time.Now()))
		m = newTM.(Model)
		
		elapsed := time.Since(iterStart)
		iterations++
		
		if time.Since(lastLog) >= 1*time.Second {
			t.Logf("Time: %v, Update time: %v, Total Iterations: %d", 
				time.Since(start).Round(time.Second), elapsed, iterations)
			lastLog = time.Now()
		}
		
		// Small sleep to not pegged CPU entirely but enough to trigger issues
		time.Sleep(1 * time.Millisecond)
	}
}
GOEOF

echo "Running Go performance test..."
go test -v internal/app/perf_test.go internal/app/model.go internal/app/model_additions.go || true

# Cleanup
rm internal/app/perf_test.go
