package app

import (
	tea "github.com/charmbracelet/bubbletea"
)

// transientHandler is the signature that all short-lived modes implement on
// Model. A handler runs only while its matching mode state is active and is
// responsible for the cancel/commit transitions of that mode.
type transientHandler func(m Model, msg tea.KeyMsg) (tea.Model, tea.Cmd)

// transientModeRoute pairs an "is this mode active?" predicate with the
// handler that should consume keystrokes while it is. The list of routes is
// the single place precedence is declared between concurrent transient modes.
type transientModeRoute struct {
	active  func(Model) bool
	handler transientHandler
}

// transientModeRoutes lists every short-lived mode in priority order. Adding
// a new pre-dispatch mode (a confirm prompt, a board-pick variant, etc.) is a
// one-line registration here.
//
// Note on shape: the contract is a (predicate, handler) pair rather than a Go
// interface because one of the existing modes — overlay.JumpMode — lives in
// internal/ui/overlay and cannot import internal/tui without creating a
// package cycle. A small route table avoids that coupling and keeps the
// JumpMode tests in their own package untouched.
func transientModeRoutes() []transientModeRoute {
	return []transientModeRoute{
		{
			active:  func(m Model) bool { return m.jumpMode != nil },
			handler: Model.handleJumpMode,
		},
		{
			active:  func(m Model) bool { return m.mergePickMode != nil },
			handler: Model.handleMergePickMode,
		},
	}
}

// routeTransientMode dispatches the key through the first matching transient
// mode. handled=false means no transient mode was active and the caller
// should continue with normal mode handling.
func (m Model) routeTransientMode(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	for _, route := range transientModeRoutes() {
		if route.active(m) {
			next, cmd := route.handler(m, msg)
			return next, cmd, true
		}
	}
	return m, nil, false
}
