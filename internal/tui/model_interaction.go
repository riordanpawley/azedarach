package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/riordanpawley/azedarach/internal/client/daemonclient"
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/riordanpawley/azedarach/internal/ui/overlay"
)

type interactionLoadedMsg struct {
	response protocol.InteractionResponseBody
	err      error
}
type interactionMutatedMsg struct {
	response protocol.InteractionResponseBody
	action   string
	err      error
}
type advisorSessionAttachedMsg struct{ err error }

func (m Model) openWaitingHumanRequest(issueID string) (tea.Model, tea.Cmd) {
	if m.daemonClient == nil {
		return m, nil
	}
	m.beginMutationFeedback("Loading human decision request")
	return m, m.loadInteractionCmd(issueID)
}

func (m Model) loadInteractionCmd(issueID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := m.daemonClient.ListInteractions(ctx, protocol.InteractionListRequestBody{IssueID: issueID})
		if err != nil {
			return interactionLoadedMsg{err: fmt.Errorf("load interaction request: %w", err)}
		}
		for _, request := range out.Requests {
			if request.Unresolved() {
				return interactionLoadedMsg{response: protocol.InteractionResponseBody{Request: request, Age: out.Ages[request.ID]}}
			}
		}
		return interactionLoadedMsg{err: fmt.Errorf("no unresolved human decision request for %s", issueID)}
	}
}

func (m Model) reloadInteractionCmd(requestID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := m.daemonClient.GetInteraction(ctx, requestID)
		if err != nil {
			return interactionLoadedMsg{err: fmt.Errorf("reload interaction request %s: %w", requestID, err)}
		}
		return interactionLoadedMsg{response: out}
	}
}

func (m Model) interactionActionCmd(action overlay.InteractionAction) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		in := protocol.InteractionMutationRequestBody{ID: action.Request.ID, ExpectedRevision: action.Request.Revision, Actor: "human", Answer: action.Answer}
		var out protocol.InteractionResponseBody
		var err error
		switch action.Kind {
		case "discuss":
			out, err = m.daemonClient.MutateInteraction(ctx, daemonclient.CommandInteractionDiscuss, in)
		case "answer":
			out, err = m.daemonClient.MutateInteraction(ctx, daemonclient.CommandInteractionAnswer, in)
		case "resolve":
			out, err = m.daemonClient.ResolveInteraction(ctx, protocol.InteractionResolveRequestBody{InteractionMutationRequestBody: in})
		case "withdraw":
			in.Reason = "withdrawn by human from TUI"
			out, err = m.daemonClient.MutateInteraction(ctx, daemonclient.CommandInteractionWithdraw, in)
		case "recover":
			out, err = m.daemonClient.MutateInteraction(ctx, daemonclient.CommandInteractionRecover, in)
		default:
			err = fmt.Errorf("unsupported interaction action %q", action.Kind)
		}
		if err != nil {
			return interactionMutatedMsg{action: action.Kind, err: err}
		}
		return interactionMutatedMsg{response: out, action: action.Kind}
	}
}

func (m Model) attachAdvisorSessionCmd(sessionID string) tea.Cmd {
	return func() tea.Msg {
		if m.tmuxClient == nil || strings.TrimSpace(sessionID) == "" {
			return advisorSessionAttachedMsg{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := m.tmuxClient.SwitchClient(ctx, sessionID); err != nil {
			return advisorSessionAttachedMsg{err: fmt.Errorf("advisor discussion started but tmux attach failed: %w", err)}
		}
		return advisorSessionAttachedMsg{}
	}
}

func (m Model) taskWaitingHuman(task *domain.Task) bool {
	return task != nil && task.IssueFacts().WaitingHuman && task.IssueFacts().WaitingHumanSource == domain.WaitingHumanSourceInteractionRequest
}

func interactionConflict(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "stale interaction revision") || strings.Contains(s, "conflict")
}
