package planning

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"

	"github.com/riordanpawley/azedarach/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockHTTPClient mocks HTTP requests
type mockHTTPClient struct {
	response *http.Response
	err      error
}

func (m *mockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return m.response, m.err
}

// mockBeadsClient mocks beads operations
type mockBeadsClient struct {
	createdTasks []domain.Task
	nextID       int
	createErr    error
	depErr       error
}

func (m *mockBeadsClient) Create(ctx context.Context, title, description string, taskType domain.TaskType, priority int, design, acceptance string, estimate *int) (*domain.Task, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}

	m.nextID++
	task := &domain.Task{
		ID:          "az-" + string(rune('0'+m.nextID)),
		Title:       title,
		Description: description,
		Type:        taskType,
		Priority:    domain.Priority(priority),
		Status:      domain.StatusOpen,
	}
	m.createdTasks = append(m.createdTasks, *task)
	return task, nil
}

func (m *mockBeadsClient) AddDependency(ctx context.Context, childID, parentID, depType string) error {
	return m.depErr
}

func createMockAPIResponse(text string) *http.Response {
	resp := map[string]interface{}{
		"content": []map[string]interface{}{
			{"type": "text", "text": text},
		},
	}
	body, _ := json.Marshal(resp)
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestNewService(t *testing.T) {
	tests := []struct {
		name    string
		apiKey  string
		wantErr bool
	}{
		{
			name:    "valid api key",
			apiKey:  "test-key",
			wantErr: false,
		},
		{
			name:    "missing api key",
			apiKey:  "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.apiKey != "" {
				os.Setenv("ANTHROPIC_API_KEY", tt.apiKey)
				defer os.Unsetenv("ANTHROPIC_API_KEY")
			} else {
				os.Unsetenv("ANTHROPIC_API_KEY")
			}

			svc, err := NewService(&mockHTTPClient{}, &mockBeadsClient{}, slog.Default())

			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, svc)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, svc)
				assert.Equal(t, domain.PlanningIdle, svc.state.Status)
			}
		})
	}
}

func TestService_GeneratePlan(t *testing.T) {
	tests := []struct {
		name        string
		description string
		response    string
		httpErr     error
		wantErr     bool
		wantTasks   int
	}{
		{
			name:        "valid plan generation",
			description: "Add user authentication",
			response: `{
				"epicTitle": "User Authentication",
				"epicDescription": "Implement secure user authentication system",
				"summary": "Add login, logout, and session management",
				"tasks": [
					{
						"id": "task-1",
						"title": "Create user model",
						"description": "Define user schema",
						"type": "task",
						"priority": 1,
						"dependsOn": [],
						"canParallelize": true,
						"design": "Use bcrypt for passwords",
						"acceptance": "User model created and tested"
					},
					{
						"id": "task-2",
						"title": "Implement login endpoint",
						"description": "Create /login API",
						"type": "task",
						"priority": 1,
						"dependsOn": ["task-1"],
						"canParallelize": false,
						"design": "JWT tokens for auth",
						"acceptance": "Login works with valid credentials"
					}
				],
				"parallelizationScore": 50
			}`,
			wantTasks: 2,
		},
		{
			name:        "invalid JSON response",
			description: "Add feature",
			response:    "not json",
			wantErr:     true,
		},
		{
			name:        "HTTP error",
			description: "Add feature",
			httpErr:     errors.New("network error"),
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("ANTHROPIC_API_KEY", "test-key")
			defer os.Unsetenv("ANTHROPIC_API_KEY")

			httpClient := &mockHTTPClient{
				response: createMockAPIResponse(tt.response),
				err:      tt.httpErr,
			}

			svc, err := NewService(httpClient, &mockBeadsClient{}, slog.Default())
			require.NoError(t, err)

			plan, err := svc.GeneratePlan(context.Background(), tt.description)

			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, plan)
				assert.Equal(t, domain.PlanningErrorStatus, svc.state.Status)
			} else {
				require.NoError(t, err)
				require.NotNil(t, plan)
				assert.Len(t, plan.Tasks, tt.wantTasks)
				assert.Equal(t, domain.PlanningReviewing, svc.state.Status)
				assert.Equal(t, tt.description, svc.state.FeatureDescription)
			}
		})
	}
}

func TestService_ReviewPlan(t *testing.T) {
	tests := []struct {
		name     string
		response string
		httpErr  error
		wantErr  bool
	}{
		{
			name: "valid review",
			response: `{
				"score": 85,
				"issues": ["Task 3 is too large"],
				"suggestions": ["Split task 3"],
				"parallelizationOpportunities": ["Tasks 1 and 2 can run in parallel"],
				"tasksTooLarge": ["task-3"],
				"missingDependencies": [],
				"isApproved": true
			}`,
		},
		{
			name:     "invalid JSON",
			response: "bad json",
			wantErr:  true,
		},
		{
			name:    "HTTP error",
			httpErr: errors.New("network error"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("ANTHROPIC_API_KEY", "test-key")
			defer os.Unsetenv("ANTHROPIC_API_KEY")

			httpClient := &mockHTTPClient{
				response: createMockAPIResponse(tt.response),
				err:      tt.httpErr,
			}

			svc, err := NewService(httpClient, &mockBeadsClient{}, slog.Default())
			require.NoError(t, err)

			plan := &domain.Plan{
				EpicTitle:       "Test Epic",
				EpicDescription: "Test",
				Summary:         "Summary",
				Tasks:           []domain.PlannedTask{},
			}

			feedback, err := svc.ReviewPlan(context.Background(), plan)

			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, feedback)
			} else {
				require.NoError(t, err)
				require.NotNil(t, feedback)
				assert.True(t, feedback.Score > 0)
			}
		})
	}
}

func TestService_RefinePlan(t *testing.T) {
	os.Setenv("ANTHROPIC_API_KEY", "test-key")
	defer os.Unsetenv("ANTHROPIC_API_KEY")

	response := `{
		"epicTitle": "Refined Epic",
		"epicDescription": "Refined description",
		"summary": "Refined summary",
		"tasks": [
			{
				"id": "task-1",
				"title": "Refined task",
				"description": "Refined",
				"type": "task",
				"priority": 1,
				"dependsOn": [],
				"canParallelize": true,
				"design": "Design",
				"acceptance": "Acceptance"
			}
		],
		"parallelizationScore": 80
	}`

	httpClient := &mockHTTPClient{
		response: createMockAPIResponse(response),
	}

	svc, err := NewService(httpClient, &mockBeadsClient{}, slog.Default())
	require.NoError(t, err)

	plan := &domain.Plan{
		EpicTitle:       "Original Epic",
		EpicDescription: "Original",
		Summary:         "Summary",
		Tasks:           []domain.PlannedTask{},
	}

	feedback := &domain.ReviewFeedback{
		Score:      70,
		Issues:     []string{"Issue"},
		IsApproved: false,
	}

	refinedPlan, err := svc.RefinePlan(context.Background(), plan, feedback)

	require.NoError(t, err)
	require.NotNil(t, refinedPlan)
	assert.Equal(t, "Refined Epic", refinedPlan.EpicTitle)
	assert.Equal(t, 1, len(refinedPlan.Tasks))
	assert.Equal(t, domain.PlanningRefining, svc.state.Status)
}

func TestService_CreateBeadsFromPlan(t *testing.T) {
	tests := []struct {
		name       string
		plan       *domain.Plan
		createErr  error
		wantBeads  int
		wantErr    bool
	}{
		{
			name: "simple plan with no dependencies",
			plan: &domain.Plan{
				EpicTitle:       "Test Epic",
				EpicDescription: "Description",
				Summary:         "Summary",
				Tasks: []domain.PlannedTask{
					{
						ID:             "task-1",
						Title:          "Task 1",
						Description:    "Desc 1",
						Type:           domain.TypeTask,
						Priority:       1,
						DependsOn:      []string{},
						CanParallelize: true,
						Design:         "Design 1",
						Acceptance:     "Accept 1",
					},
					{
						ID:             "task-2",
						Title:          "Task 2",
						Description:    "Desc 2",
						Type:           domain.TypeTask,
						Priority:       2,
						DependsOn:      []string{},
						CanParallelize: true,
						Design:         "Design 2",
						Acceptance:     "Accept 2",
					},
				},
			},
			wantBeads: 3, // 1 epic + 2 tasks
		},
		{
			name: "plan with dependencies",
			plan: &domain.Plan{
				EpicTitle:       "Test Epic",
				EpicDescription: "Description",
				Summary:         "Summary",
				Tasks: []domain.PlannedTask{
					{
						ID:             "task-1",
						Title:          "Task 1",
						Description:    "Desc 1",
						Type:           domain.TypeTask,
						Priority:       1,
						DependsOn:      []string{},
						CanParallelize: true,
						Design:         "Design 1",
						Acceptance:     "Accept 1",
					},
					{
						ID:             "task-2",
						Title:          "Task 2",
						Description:    "Desc 2",
						Type:           domain.TypeTask,
						Priority:       2,
						DependsOn:      []string{"task-1"},
						CanParallelize: false,
						Design:         "Design 2",
						Acceptance:     "Accept 2",
					},
				},
			},
			wantBeads: 3, // 1 epic + 2 tasks
		},
		{
			name: "epic creation error",
			plan: &domain.Plan{
				EpicTitle:       "Test Epic",
				EpicDescription: "Description",
				Summary:         "Summary",
				Tasks:           []domain.PlannedTask{},
			},
			createErr: errors.New("creation failed"),
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("ANTHROPIC_API_KEY", "test-key")
			defer os.Unsetenv("ANTHROPIC_API_KEY")

			beadsClient := &mockBeadsClient{
				createdTasks: []domain.Task{},
				createErr:    tt.createErr,
			}

			svc, err := NewService(&mockHTTPClient{}, beadsClient, slog.Default())
			require.NoError(t, err)

			beads, err := svc.CreateBeadsFromPlan(context.Background(), tt.plan)

			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, beads)
				assert.Equal(t, domain.PlanningErrorStatus, svc.state.Status)
			} else {
				require.NoError(t, err)
				assert.Len(t, beads, tt.wantBeads)
				assert.Equal(t, domain.PlanningComplete, svc.state.Status)
			}
		})
	}
}

func TestService_RunPlanningWorkflow(t *testing.T) {
	os.Setenv("ANTHROPIC_API_KEY", "test-key")
	defer os.Unsetenv("ANTHROPIC_API_KEY")

	// Mock sequence of API responses
	responses := []string{
		// Initial plan generation
		`{
			"epicTitle": "Test Feature",
			"epicDescription": "Test description",
			"summary": "Test summary",
			"tasks": [
				{
					"id": "task-1",
					"title": "Task 1",
					"description": "Desc",
					"type": "task",
					"priority": 1,
					"dependsOn": [],
					"canParallelize": true,
					"design": "Design",
					"acceptance": "Accept"
				}
			],
			"parallelizationScore": 80
		}`,
		// Review response
		`{
			"score": 90,
			"issues": [],
			"suggestions": [],
			"parallelizationOpportunities": [],
			"tasksTooLarge": [],
			"missingDependencies": [],
			"isApproved": true
		}`,
	}

	// Custom mock that changes response each call
	httpClient := &mockHTTPClientWithSequence{
		responses: responses,
	}

	beadsClient := &mockBeadsClient{
		createdTasks: []domain.Task{},
	}

	svc, err := NewService(httpClient, beadsClient, slog.Default())
	require.NoError(t, err)

	beads, err := svc.RunPlanningWorkflow(context.Background(), "Add test feature")

	require.NoError(t, err)
	assert.NotNil(t, beads)
	assert.Equal(t, domain.PlanningComplete, svc.state.Status)
	assert.Len(t, svc.state.ReviewHistory, 1)
	assert.True(t, svc.state.ReviewHistory[0].IsApproved)
}

// mockHTTPClientWithSequence returns different responses in sequence
type mockHTTPClientWithSequence struct {
	responses []string
	callCount int
}

func (m *mockHTTPClientWithSequence) Do(req *http.Request) (*http.Response, error) {
	if m.callCount < len(m.responses) {
		response := createMockAPIResponse(m.responses[m.callCount])
		m.callCount++
		return response, nil
	}
	return &http.Response{
		StatusCode: 500,
		Body:       io.NopCloser(bytes.NewReader([]byte("no more responses"))),
		Header:     make(http.Header),
	}, nil
}

func TestService_Reset(t *testing.T) {
	os.Setenv("ANTHROPIC_API_KEY", "test-key")
	defer os.Unsetenv("ANTHROPIC_API_KEY")

	svc, err := NewService(&mockHTTPClient{}, &mockBeadsClient{}, slog.Default())
	require.NoError(t, err)

	// Modify state
	svc.state.Status = domain.PlanningGenerating
	svc.state.FeatureDescription = "Test"
	svc.state.ReviewPass = 3

	// Reset
	svc.Reset()

	// Verify reset
	assert.Equal(t, domain.PlanningIdle, svc.state.Status)
	assert.Empty(t, svc.state.FeatureDescription)
	assert.Equal(t, 0, svc.state.ReviewPass)
}

func TestParseJSONResponse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:  "plain JSON",
			input: `{"key": "value"}`,
		},
		{
			name:  "JSON in code block",
			input: "```json\n{\"key\": \"value\"}\n```",
		},
		{
			name:  "JSON in code block without language",
			input: "```\n{\"key\": \"value\"}\n```",
		},
		{
			name:    "invalid JSON",
			input:   "not json",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result map[string]string
			err := parseJSONResponse(tt.input, &result)

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, "value", result["key"])
			}
		})
	}
}
