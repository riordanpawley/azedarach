package planning

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/domain"
)

const (
	anthropicAPIURL = "https://api.anthropic.com/v1/messages"
	anthropicModel  = "claude-sonnet-4-20250514"
	maxTokens       = 8192
)

// prompts for Claude API
const generationPrompt = `You are an expert software architect creating a development plan.

Given the feature description, create a detailed implementation plan optimized for parallel development by multiple AI coding agents (Claude Code sessions).

CRITICAL REQUIREMENTS:
1. **Small Tasks**: Each task should be completable in 30 minutes to 2 hours. If larger, split it.
2. **Independence**: Maximize tasks that can run in parallel without blocking each other.
3. **Clear Boundaries**: Each task should touch a distinct set of files to avoid merge conflicts.
4. **Explicit Dependencies**: Only add dependencies where truly necessary (shared types, APIs, etc.)
5. **Design Notes**: Include specific implementation guidance for each task.

Output a JSON object matching this schema:
{
  "epicTitle": "Brief title for the epic",
  "epicDescription": "Detailed description of the feature",
  "summary": "Brief summary of the implementation approach",
  "tasks": [
    {
      "id": "task-1",
      "title": "Concise task title",
      "description": "What this task accomplishes",
      "type": "task|bug|feature|chore",
      "priority": 1-4,
      "estimate": hours (optional),
      "dependsOn": ["task-id", ...],
      "canParallelize": true|false,
      "design": "Technical implementation notes",
      "acceptance": "How to verify completion"
    }
  ],
  "parallelizationScore": 0-100
}

Feature description:
`

const reviewPrompt = `You are reviewing a development plan for quality and parallelization.

Evaluate this plan against these criteria:
1. **Task Size**: Are all tasks small enough (30min-2hr)? Flag any that are too large.
2. **Parallelization**: What percentage of tasks can run independently? Suggest improvements.
3. **Dependencies**: Are dependencies minimal and correct? Flag missing or unnecessary ones.
4. **Clarity**: Is each task's scope clear? Are there ambiguities?
5. **Completeness**: Does the plan cover all aspects of the feature?

Output a JSON review:
{
  "score": 0-100,
  "issues": ["issue1", "issue2"],
  "suggestions": ["suggestion1", "suggestion2"],
  "parallelizationOpportunities": ["opportunity1"],
  "tasksTooLarge": ["task-id1", "task-id2"],
  "missingDependencies": [
    {"taskId": "task-x", "shouldDependOn": "task-y", "reason": "why"}
  ],
  "isApproved": true|false
}

Current plan:
`

const refinementPrompt = `You are refining a development plan based on review feedback.

Apply the suggested improvements while maintaining:
1. Maximum parallelization
2. Small, focused tasks (30min-2hr each)
3. Minimal, correct dependencies
4. Clear scope boundaries

Review feedback to address:
{FEEDBACK}

Current plan:
{PLAN}

Output the refined plan in the same JSON format as the original.`

// HTTPClient abstracts HTTP requests for testing
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// BeadsClient interface for creating beads
type BeadsClient interface {
	Create(ctx context.Context, title, description string, taskType domain.TaskType, priority int, design, acceptance string, estimate *int) (*domain.Task, error)
	AddDependency(ctx context.Context, childID, parentID, depType string) error
}

// Service provides AI-powered task planning
type Service struct {
	httpClient  HTTPClient
	beadsClient BeadsClient
	logger      *slog.Logger
	apiKey      string
	state       *domain.PlanningState
}

// NewService creates a new planning service
func NewService(httpClient HTTPClient, beadsClient BeadsClient, logger *slog.Logger) (*Service, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return nil, errors.New("ANTHROPIC_API_KEY environment variable not set")
	}

	return &Service{
		httpClient:  httpClient,
		beadsClient: beadsClient,
		logger:      logger,
		apiKey:      apiKey,
		state: &domain.PlanningState{
			Status:          domain.PlanningIdle,
			MaxReviewPasses: 5,
			ReviewHistory:   []domain.ReviewFeedback{},
			CreatedBeads:    []domain.Task{},
			UpdatedAt:       time.Now(),
		},
	}, nil
}

// GetState returns the current planning state
func (s *Service) GetState() domain.PlanningState {
	return *s.state
}

// Reset resets the planning state
func (s *Service) Reset() {
	s.state = &domain.PlanningState{
		Status:          domain.PlanningIdle,
		MaxReviewPasses: 5,
		ReviewHistory:   []domain.ReviewFeedback{},
		CreatedBeads:    []domain.Task{},
		UpdatedAt:       time.Now(),
	}
}

// anthropicRequest represents a request to the Anthropic API
type anthropicRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	Messages  []message `json:"messages"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// anthropicResponse represents a response from the Anthropic API
type anthropicResponse struct {
	Content []content `json:"content"`
}

type content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// callClaude makes a request to the Claude API
func (s *Service) callClaude(ctx context.Context, prompt string) (string, error) {
	reqBody := anthropicRequest{
		Model:     anthropicModel,
		MaxTokens: maxTokens,
		Messages: []message{
			{
				Role:    "user",
				Content: prompt,
			},
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", anthropicAPIURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", s.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var apiResp anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if len(apiResp.Content) == 0 {
		return "", errors.New("empty response from Claude API")
	}

	for _, c := range apiResp.Content {
		if c.Type == "text" {
			return c.Text, nil
		}
	}

	return "", errors.New("no text content in Claude response")
}

// parseJSONResponse extracts and parses JSON from Claude response
func parseJSONResponse(text string, target interface{}) error {
	// Extract JSON from potential markdown code blocks
	jsonStr := strings.TrimSpace(text)

	// Handle ```json ... ``` blocks
	jsonRegex := regexp.MustCompile("```(?:json)?\\s*([\\s\\S]*?)```")
	if matches := jsonRegex.FindStringSubmatch(jsonStr); len(matches) > 1 {
		jsonStr = strings.TrimSpace(matches[1])
	}

	// Parse JSON
	if err := json.Unmarshal([]byte(jsonStr), target); err != nil {
		return fmt.Errorf("failed to parse JSON: %w", err)
	}

	return nil
}

// GeneratePlan generates an initial plan from a feature description
func (s *Service) GeneratePlan(ctx context.Context, featureDescription string) (*domain.Plan, error) {
	s.logger.Info("generating plan", "description", featureDescription)

	s.state.Status = domain.PlanningGenerating
	s.state.FeatureDescription = featureDescription
	s.state.UpdatedAt = time.Now()

	prompt := generationPrompt + featureDescription
	response, err := s.callClaude(ctx, prompt)
	if err != nil {
		s.state.Status = domain.PlanningErrorStatus
		s.state.Error = err.Error()
		s.state.UpdatedAt = time.Now()
		return nil, &domain.PlanningError{
			Phase:   "generation",
			Message: "failed to call Claude API",
			Err:     err,
		}
	}

	var plan domain.Plan
	if err := parseJSONResponse(response, &plan); err != nil {
		s.state.Status = domain.PlanningErrorStatus
		s.state.Error = err.Error()
		s.state.UpdatedAt = time.Now()
		return nil, &domain.PlanningError{
			Phase:   "generation",
			Message: "failed to parse plan response",
			Err:     err,
		}
	}

	s.state.Status = domain.PlanningReviewing
	s.state.CurrentPlan = &plan
	s.state.UpdatedAt = time.Now()

	s.logger.Info("plan generated", "tasks", len(plan.Tasks))
	return &plan, nil
}

// ReviewPlan reviews a plan and returns feedback
func (s *Service) ReviewPlan(ctx context.Context, plan *domain.Plan) (*domain.ReviewFeedback, error) {
	s.logger.Info("reviewing plan")

	planJSON, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal plan: %w", err)
	}

	prompt := reviewPrompt + string(planJSON)
	response, err := s.callClaude(ctx, prompt)
	if err != nil {
		return nil, &domain.PlanningError{
			Phase:   "review",
			Message: "failed to call Claude API",
			Err:     err,
		}
	}

	var feedback domain.ReviewFeedback
	if err := parseJSONResponse(response, &feedback); err != nil {
		return nil, &domain.PlanningError{
			Phase:   "review",
			Message: "failed to parse review response",
			Err:     err,
		}
	}

	s.logger.Info("plan reviewed", "score", feedback.Score, "approved", feedback.IsApproved)
	return &feedback, nil
}

// RefinePlan refines a plan based on review feedback
func (s *Service) RefinePlan(ctx context.Context, plan *domain.Plan, feedback *domain.ReviewFeedback) (*domain.Plan, error) {
	s.logger.Info("refining plan")

	s.state.Status = domain.PlanningRefining
	s.state.UpdatedAt = time.Now()

	feedbackJSON, err := json.MarshalIndent(feedback, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal feedback: %w", err)
	}

	planJSON, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal plan: %w", err)
	}

	prompt := strings.ReplaceAll(refinementPrompt, "{FEEDBACK}", string(feedbackJSON))
	prompt = strings.ReplaceAll(prompt, "{PLAN}", string(planJSON))

	response, err := s.callClaude(ctx, prompt)
	if err != nil {
		return nil, &domain.PlanningError{
			Phase:   "refinement",
			Message: "failed to call Claude API",
			Err:     err,
		}
	}

	var refinedPlan domain.Plan
	if err := parseJSONResponse(response, &refinedPlan); err != nil {
		return nil, &domain.PlanningError{
			Phase:   "refinement",
			Message: "failed to parse refined plan",
			Err:     err,
		}
	}

	s.logger.Info("plan refined", "tasks", len(refinedPlan.Tasks))
	return &refinedPlan, nil
}

// CreateBeadsFromPlan creates beads from a finalized plan
func (s *Service) CreateBeadsFromPlan(ctx context.Context, plan *domain.Plan) ([]domain.Task, error) {
	s.logger.Info("creating beads from plan")

	s.state.Status = domain.PlanningCreatingBeads
	s.state.UpdatedAt = time.Now()

	createdBeads := []domain.Task{}
	idMapping := make(map[string]string) // Map temp IDs to real bead IDs

	// 1. Create the epic first
	s.logger.Debug("creating epic", "title", plan.EpicTitle)
	epic, err := s.beadsClient.Create(
		ctx,
		plan.EpicTitle,
		plan.EpicDescription,
		domain.TypeEpic,
		1,
		plan.Summary,
		"",
		nil,
	)
	if err != nil {
		s.state.Status = domain.PlanningErrorStatus
		s.state.Error = err.Error()
		s.state.UpdatedAt = time.Now()
		return nil, &domain.PlanningError{
			Phase:   "beads_creation",
			Message: "failed to create epic",
			Err:     err,
		}
	}

	createdBeads = append(createdBeads, *epic)

	// 2. Create tasks in dependency order
	// First, create tasks with no dependencies
	var noDeps, withDeps []domain.PlannedTask
	for _, task := range plan.Tasks {
		if len(task.DependsOn) == 0 {
			noDeps = append(noDeps, task)
		} else {
			withDeps = append(withDeps, task)
		}
	}

	// Create tasks without dependencies
	for _, task := range noDeps {
		s.logger.Debug("creating task", "title", task.Title)
		bead, err := s.beadsClient.Create(
			ctx,
			task.Title,
			task.Description,
			task.Type,
			task.Priority,
			task.Design,
			task.Acceptance,
			task.Estimate,
		)
		if err != nil {
			s.logger.Warn("failed to create task", "title", task.Title, "error", err)
			continue
		}

		idMapping[task.ID] = bead.ID
		createdBeads = append(createdBeads, *bead)

		// Link to epic as child
		if err := s.beadsClient.AddDependency(ctx, bead.ID, epic.ID, "parent-child"); err != nil {
			s.logger.Warn("failed to link task to epic", "task", bead.ID, "error", err)
		}
	}

	// Create tasks with dependencies (may need multiple passes)
	remaining := withDeps
	maxIterations := 10

	for len(remaining) > 0 && maxIterations > 0 {
		maxIterations--
		var canCreate, stillWaiting []domain.PlannedTask

		for _, task := range remaining {
			allDepsResolved := true
			for _, depID := range task.DependsOn {
				if _, ok := idMapping[depID]; !ok {
					allDepsResolved = false
					break
				}
			}

			if allDepsResolved {
				canCreate = append(canCreate, task)
			} else {
				stillWaiting = append(stillWaiting, task)
			}
		}

		for _, task := range canCreate {
			s.logger.Debug("creating task with dependencies", "title", task.Title)
			bead, err := s.beadsClient.Create(
				ctx,
				task.Title,
				task.Description,
				task.Type,
				task.Priority,
				task.Design,
				task.Acceptance,
				task.Estimate,
			)
			if err != nil {
				s.logger.Warn("failed to create task", "title", task.Title, "error", err)
				continue
			}

			idMapping[task.ID] = bead.ID
			createdBeads = append(createdBeads, *bead)

			// Link to epic as child
			if err := s.beadsClient.AddDependency(ctx, bead.ID, epic.ID, "parent-child"); err != nil {
				s.logger.Warn("failed to link task to epic", "task", bead.ID, "error", err)
			}

			// Add task dependencies (blocks relationship)
			for _, depID := range task.DependsOn {
				if realDepID, ok := idMapping[depID]; ok {
					if err := s.beadsClient.AddDependency(ctx, bead.ID, realDepID, "blocks"); err != nil {
						s.logger.Warn("failed to add dependency", "task", bead.ID, "dep", realDepID, "error", err)
					}
				}
			}
		}

		remaining = stillWaiting
	}

	if len(remaining) > 0 {
		s.logger.Warn("could not resolve dependencies", "count", len(remaining))
	}

	s.state.Status = domain.PlanningComplete
	s.state.CreatedBeads = createdBeads
	s.state.UpdatedAt = time.Now()

	s.logger.Info("beads created", "count", len(createdBeads))
	return createdBeads, nil
}

// RunPlanningWorkflow runs the complete planning workflow
func (s *Service) RunPlanningWorkflow(ctx context.Context, featureDescription string) ([]domain.Task, error) {
	s.logger.Info("starting planning workflow", "description", featureDescription)

	// 1. Generate initial plan
	plan, err := s.GeneratePlan(ctx, featureDescription)
	if err != nil {
		return nil, err
	}

	// 2. Review and refine loop
	for pass := 1; pass <= s.state.MaxReviewPasses; pass++ {
		s.state.ReviewPass = pass
		s.state.UpdatedAt = time.Now()

		feedback, err := s.ReviewPlan(ctx, plan)
		if err != nil {
			s.state.Status = domain.PlanningErrorStatus
			s.state.Error = err.Error()
			s.state.UpdatedAt = time.Now()
			return nil, err
		}

		s.state.ReviewHistory = append(s.state.ReviewHistory, *feedback)
		s.state.UpdatedAt = time.Now()

		if feedback.IsApproved {
			s.logger.Info("plan approved", "pass", pass)
			break
		}

		if pass < s.state.MaxReviewPasses {
			plan, err = s.RefinePlan(ctx, plan, feedback)
			if err != nil {
				s.state.Status = domain.PlanningErrorStatus
				s.state.Error = err.Error()
				s.state.UpdatedAt = time.Now()
				return nil, err
			}
			s.state.CurrentPlan = plan
			s.state.UpdatedAt = time.Now()
		}
	}

	// 3. Create beads from the final plan
	return s.CreateBeadsFromPlan(ctx, plan)
}
