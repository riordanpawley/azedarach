package overlay

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/riordanpawley/azedarach/internal/domain"
)

const InteractionActionKey = "interaction-action"

type InteractionAction struct {
	Kind    string
	Request domain.InteractionRequest
	Answer  domain.InteractionAnswerPayload
}

// InteractionOverlay presents one daemon-owned human decision request. It
// intentionally contains no transition policy; actions carry the displayed
// revision back to the TUI's typed daemon client.
type InteractionOverlay struct {
	dialogViewportState
	request       domain.InteractionRequest
	age           domain.InteractionAgeView
	styles        *Styles
	cursor        int
	editing       bool
	resolve       bool
	confirming    bool
	confirmAction string
	editor        textarea.Model
}

func NewInteractionOverlay(request domain.InteractionRequest, age domain.InteractionAgeView) *InteractionOverlay {
	ed := textarea.New()
	ed.Placeholder = "Explain the answer and any constraints…"
	ed.CharLimit = 4000
	ed.SetWidth(80)
	ed.SetHeight(6)
	if request.Proposal != nil {
		ed.SetValue(request.Proposal.Answer.Rationale)
	}
	o := &InteractionOverlay{request: request, age: age, styles: New(), editor: ed}
	if request.Proposal != nil {
		o.cursor = optionIndex(request, request.Proposal.Answer.SelectedOption)
	}
	return o
}

func (o *InteractionOverlay) Init() tea.Cmd     { return nil }
func (o *InteractionOverlay) Title() string     { return "Waiting Human" }
func (o *InteractionOverlay) RequestID() string { return o.request.ID }
func (o *InteractionOverlay) Size() (int, int)  { return o.ClampResponsive(92, 30) }

func (o *InteractionOverlay) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		o.ApplyWindowSize(size)
		w, _ := o.Size()
		o.editor.SetWidth(max(20, w-8))
		return o, nil
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return o, nil
	}
	if o.confirming {
		switch key.String() {
		case "y", "Y":
			o.confirming = false
			kind := o.confirmAction
			o.confirmAction = ""
			return o, actionCmd(kind, o.request, o.answer())
		case "n", "N", "esc":
			o.confirming = false
			o.confirmAction = ""
		}
		return o, nil
	}
	if o.editing {
		switch key.String() {
		case "esc":
			o.editing, o.resolve = false, false
			o.editor.Blur()
			return o, nil
		case "ctrl+s":
			a := o.answer()
			if strings.TrimSpace(a.Rationale) == "" {
				return o, nil
			}
			kind := "answer"
			if o.resolve {
				kind = "resolve"
			}
			if o.request.Significance != domain.InteractionSignificanceRoutine {
				o.confirming = true
				o.confirmAction = kind
				return o, nil
			}
			return o, func() tea.Msg {
				return SelectionMsg{Key: InteractionActionKey, Value: InteractionAction{Kind: kind, Request: o.request, Answer: a}}
			}
		}
		var cmd tea.Cmd
		o.editor, cmd = o.editor.Update(msg)
		return o, cmd
	}
	switch key.String() {
	case "esc", "q":
		return o, func() tea.Msg { return CloseOverlayMsg{} }
	case "j", "down":
		if o.cursor < len(o.request.Options)-1 {
			o.cursor++
		}
	case "k", "up":
		if o.cursor > 0 {
			o.cursor--
		}
	case "a":
		o.editing = true
		o.resolve = false
		o.editor.Focus()
		return o, textarea.Blink
	case "enter", "e":
		o.editing = true
		o.resolve = true
		o.editor.Focus()
		return o, textarea.Blink
	case "d":
		return o, actionCmd("discuss", o.request, domain.InteractionAnswerPayload{})
	case "w":
		o.confirming = true
		o.confirmAction = "withdraw"
		return o, nil
	case "r":
		if o.age.Stale {
			return o, actionCmd("recover", o.request, domain.InteractionAnswerPayload{})
		}
	}
	return o, nil
}

func actionCmd(kind string, request domain.InteractionRequest, answer domain.InteractionAnswerPayload) tea.Cmd {
	return func() tea.Msg {
		return SelectionMsg{Key: InteractionActionKey, Value: InteractionAction{Kind: kind, Request: request, Answer: answer}}
	}
}

func (o *InteractionOverlay) answer() domain.InteractionAnswerPayload {
	selected := ""
	if o.cursor >= 0 && o.cursor < len(o.request.Options) {
		selected = o.request.Options[o.cursor].Key
	}
	answer := domain.InteractionAnswerPayload{SelectedOption: selected, Rationale: strings.TrimSpace(o.editor.Value()), SignificanceRecommendation: o.request.Significance, Revision: o.request.Revision}
	if o.request.Proposal != nil {
		answer.Constraints = append([]string(nil), o.request.Proposal.Answer.Constraints...)
	}
	return answer
}

func optionIndex(r domain.InteractionRequest, key string) int {
	for i := range r.Options {
		if r.Options[i].Key == key {
			return i
		}
	}
	return 0
}

func (o *InteractionOverlay) View() string {
	w, h := o.Size()
	return renderDialogTwoPane(dialogLayoutConfig{styles: o.styles, width: w, height: h, title: "Waiting Human · whole issue blocked", breakpoint: 84, gap: 2, minLeft: 42, minRight: 24, leftFocused: true, rightSectionTitle: "Decision", renderLeft: func(_ dialogLayoutMode, width, height int) string { return o.details(width, height) }, renderRight: func(_ dialogLayoutMode, width, height int) string { return o.actions(width, height) }})
}

func (o *InteractionOverlay) details(width, height int) string {
	lines := []string{o.styles.MenuItemActive.Render(o.request.Question), "", "Why", o.request.Why}
	if strings.TrimSpace(o.request.Context) != "" {
		lines = append(lines, "", "Context", o.request.Context)
	}
	lines = append(lines, "", "Options")
	for i, option := range o.request.Options {
		mark := "  "
		if i == o.cursor {
			mark = "› "
		}
		lines = append(lines, fmt.Sprintf("%s%s — %s", mark, option.Label, option.Description))
	}
	for _, required := range o.request.RequiredDecisions {
		lines = append(lines, "• "+required)
	}
	return lipgloss.NewStyle().Width(max(1, width-2)).MaxHeight(height).Render(strings.Join(lines, "\n"))
}

func (o *InteractionOverlay) actions(width, height int) string {
	age := (time.Duration(o.age.AgeSeconds) * time.Second).Round(time.Second).String()
	meta := fmt.Sprintf("Issue %s\nState %s\nAge %s\nRevision %d\nSignificance %s", o.request.IssueID, o.request.State, age, o.request.Revision, o.request.Significance)
	if o.age.Stale {
		meta += " · STALE"
	}
	packet := "\n\nRecommendation\n" + o.request.DecisionPacket.Recommendation
	if o.request.Proposal != nil {
		packet += "\n\nAI proposal\n" + o.request.Proposal.Answer.Rationale
	}
	if o.confirming {
		prompt := "Confirm significant resolution?\nThis may create or amend durable decisions or requirements."
		if o.confirmAction == "withdraw" {
			prompt = "Withdraw this request?\nThe issue will no longer be blocked, but no answer will be accepted."
		}
		return meta + packet + "\n\n" + prompt + "\n\ny confirm · n cancel"
	}
	if o.editing {
		return meta + packet + "\n\n" + o.editor.View() + "\nctrl+s submit · esc cancel"
	}
	return lipgloss.NewStyle().Width(max(1, width-1)).MaxHeight(height).Render(meta + packet + "\n\n↑/↓ choose · a answer\ne/enter resolve · d discuss\nr recover stale · w withdraw\nesc close")
}
