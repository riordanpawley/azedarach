package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/riordanpawley/azedarach/internal/config"
	"github.com/riordanpawley/azedarach/internal/domain"
)

func TestRunCLIReadyJSONSuccessIncludesCommandOKAndResultItems(t *testing.T) {
	stubQueryDependencies(
		t,
		func(_ []reflect.Value) ([]domain.Task, error) {
			return []domain.Task{
				newQueryTask("AZE-401", domain.StatusOpen),
				newQueryTask("AZE-402", domain.StatusInProgress),
			}, nil
		},
		func(_ []reflect.Value) ([]domain.Task, error) {
			return []domain.Task{}, nil
		},
	)

	exitCode, stdout, stderr := runCLIForTest([]string{"ready", "--json"})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %q)", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}

	var envelope struct {
		Command string `json:"command"`
		OK      bool   `json:"ok"`
		Result  struct {
			Items []json.RawMessage `json:"items"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("expected valid JSON output, got parse error: %v\noutput:\n%s", err, stdout)
	}

	if envelope.Command != "ready" {
		t.Fatalf("expected command=ready, got %q", envelope.Command)
	}
	if !envelope.OK {
		t.Fatalf("expected ok=true, got false")
	}
	if len(envelope.Result.Items) == 0 {
		t.Fatalf("expected result.items to be non-empty")
	}
}

func TestRunCLIBlockedJSONSuccessFiltersToBlockedStatusesOnly(t *testing.T) {
	stubQueryDependencies(
		t,
		func(_ []reflect.Value) ([]domain.Task, error) {
			return []domain.Task{
				newQueryTask("AZE-410", domain.StatusOpen),
				newQueryTask("AZE-411", domain.StatusBlocked),
				newQueryTask("AZE-412", domain.StatusInProgress),
				newQueryTask("AZE-413", domain.StatusBlocked),
			}, nil
		},
		func(_ []reflect.Value) ([]domain.Task, error) {
			return []domain.Task{}, nil
		},
	)

	exitCode, stdout, stderr := runCLIForTest([]string{"blocked", "--json"})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %q)", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}

	var envelope struct {
		Command string `json:"command"`
		OK      bool   `json:"ok"`
		Result  struct {
			Items []struct {
				Status string `json:"status"`
			} `json:"items"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("expected valid JSON output, got parse error: %v\noutput:\n%s", err, stdout)
	}

	if envelope.Command != "blocked" {
		t.Fatalf("expected command=blocked, got %q", envelope.Command)
	}
	if !envelope.OK {
		t.Fatalf("expected ok=true, got false")
	}
	if len(envelope.Result.Items) == 0 {
		t.Fatalf("expected blocked result.items to be non-empty")
	}

	for _, item := range envelope.Result.Items {
		if item.Status != string(domain.StatusBlocked) {
			t.Fatalf("expected blocked items only, got status %q", item.Status)
		}
	}
}

func TestRunCLISearchJSONSuccessIncludesCommandAndOK(t *testing.T) {
	const expectedQuery = "auth"

	var capturedQuery string
	stubQueryDependencies(
		t,
		func(_ []reflect.Value) ([]domain.Task, error) {
			return []domain.Task{}, nil
		},
		func(args []reflect.Value) ([]domain.Task, error) {
			if len(args) > 0 && args[0].Kind() == reflect.String {
				capturedQuery = args[0].String()
			}
			return []domain.Task{
				newQueryTask("AZE-420", domain.StatusOpen),
			}, nil
		},
	)

	exitCode, stdout, stderr := runCLIForTest([]string{"search", expectedQuery, "--json"})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %q)", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}

	var envelope struct {
		Command string `json:"command"`
		OK      bool   `json:"ok"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("expected valid JSON output, got parse error: %v\noutput:\n%s", err, stdout)
	}

	if envelope.Command != "search" {
		t.Fatalf("expected command=search, got %q", envelope.Command)
	}
	if !envelope.OK {
		t.Fatalf("expected ok=true, got false")
	}
	if capturedQuery != expectedQuery {
		t.Fatalf("expected search query %q to reach search dependency, got %q", expectedQuery, capturedQuery)
	}
}

func TestRunCLICountJSONSuccessIncludesCommandOKAndNumericCount(t *testing.T) {
	stubQueryDependencies(
		t,
		func(_ []reflect.Value) ([]domain.Task, error) {
			return []domain.Task{
				newQueryTask("AZE-430", domain.StatusOpen),
				newQueryTask("AZE-431", domain.StatusBlocked),
				newQueryTask("AZE-432", domain.StatusDone),
			}, nil
		},
		func(_ []reflect.Value) ([]domain.Task, error) {
			return []domain.Task{}, nil
		},
	)

	exitCode, stdout, stderr := runCLIForTest([]string{"count", "--json"})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %q)", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr, got %q", stderr)
	}

	var envelope struct {
		Command string `json:"command"`
		OK      bool   `json:"ok"`
		Result  struct {
			Count json.RawMessage `json:"count"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("expected valid JSON output, got parse error: %v\noutput:\n%s", err, stdout)
	}

	if envelope.Command != "count" {
		t.Fatalf("expected command=count, got %q", envelope.Command)
	}
	if !envelope.OK {
		t.Fatalf("expected ok=true, got false")
	}

	var count float64
	if err := json.Unmarshal(envelope.Result.Count, &count); err != nil {
		t.Fatalf("expected result.count to be numeric, got %s (error: %v)", string(envelope.Result.Count), err)
	}
}

func TestRunCLISearchJSONMissingQueryReturnsInvalidArgument(t *testing.T) {
	stubQueryDependencies(
		t,
		func(_ []reflect.Value) ([]domain.Task, error) {
			return []domain.Task{}, nil
		},
		func(_ []reflect.Value) ([]domain.Task, error) {
			return []domain.Task{}, nil
		},
	)

	exitCode, stdout, stderr := runCLIForTest([]string{"search", "--json"})
	if exitCode == 0 {
		t.Fatalf("expected non-zero exit code for missing query")
	}
	if stderr != "" {
		t.Fatalf("expected empty stderr in JSON mode, got %q", stderr)
	}

	var envelope struct {
		Command string `json:"command"`
		OK      bool   `json:"ok"`
		Error   struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("expected valid JSON output, got parse error: %v\noutput:\n%s", err, stdout)
	}

	if envelope.Command != "search" {
		t.Fatalf("expected command=search, got %q", envelope.Command)
	}
	if envelope.OK {
		t.Fatalf("expected ok=false for missing query")
	}
	if envelope.Error.Code != "invalid_argument" {
		t.Fatalf("expected error.code=invalid_argument, got %q", envelope.Error.Code)
	}
}

func newQueryTask(id string, status domain.Status) domain.Task {
	return domain.Task{
		ID:       id,
		Title:    "Query command test task",
		Status:   status,
		Priority: domain.P2,
		Type:     domain.TypeTask,
	}
}

func stubQueryDependencies(
	t *testing.T,
	taskResponder func(args []reflect.Value) ([]domain.Task, error),
	searchResponder func(args []reflect.Value) ([]domain.Task, error),
) {
	t.Helper()

	originalLoadConfig := loadConfig
	loadConfig = func() (*config.Config, error) {
		return config.DefaultConfig(), nil
	}

	restoreTaskFactory := stubQueryFactory(t, &newTaskQuery, taskResponder)
	restoreSearchFactory := stubQueryFactory(t, &newSearchQuery, searchResponder)

	t.Cleanup(func() {
		loadConfig = originalLoadConfig
		restoreTaskFactory()
		restoreSearchFactory()
	})
}

func stubQueryFactory[F any](
	t *testing.T,
	factory *F,
	responder func(args []reflect.Value) ([]domain.Task, error),
) func() {
	t.Helper()

	factoryValue := reflect.ValueOf(factory).Elem()
	originalFactory := reflect.ValueOf(factoryValue.Interface())
	factoryType := factoryValue.Type()

	if factoryType.Kind() != reflect.Func {
		t.Fatalf("expected query factory to be a function, got %s", factoryType.Kind())
	}
	if factoryType.NumIn() != 0 || factoryType.NumOut() != 1 {
		t.Fatalf(
			"expected query factory signature func() <query-func>, got %s",
			factoryType.String(),
		)
	}

	queryFuncType := factoryType.Out(0)
	if queryFuncType.Kind() != reflect.Func {
		t.Fatalf("expected query factory output to be a function, got %s", queryFuncType.Kind())
	}
	if queryFuncType.NumOut() != 2 {
		t.Fatalf(
			"expected query function to return (tasks, error), got %s",
			queryFuncType.String(),
		)
	}

	tasksType := queryFuncType.Out(0)
	errorType := queryFuncType.Out(1)
	expectedTasksType := reflect.TypeOf([]domain.Task(nil))
	expectedErrorInterface := reflect.TypeOf((*error)(nil)).Elem()

	if !expectedTasksType.AssignableTo(tasksType) && !expectedTasksType.ConvertibleTo(tasksType) {
		t.Fatalf(
			"expected query function first return to accept []domain.Task, got %s",
			tasksType.String(),
		)
	}
	if !errorType.Implements(expectedErrorInterface) {
		t.Fatalf(
			"expected query function second return to implement error, got %s",
			errorType.String(),
		)
	}

	queryFunc := reflect.MakeFunc(queryFuncType, func(args []reflect.Value) []reflect.Value {
		tasks, err := responder(args)

		var tasksValue reflect.Value
		if tasks == nil {
			tasksValue = reflect.Zero(tasksType)
		} else {
			tasksValue = reflect.ValueOf(tasks)
			if !tasksValue.Type().AssignableTo(tasksType) {
				tasksValue = tasksValue.Convert(tasksType)
			}
		}

		var errValue reflect.Value
		if err == nil {
			errValue = reflect.Zero(errorType)
		} else {
			errValue = reflect.ValueOf(err)
			if !errValue.Type().AssignableTo(errorType) {
				errValue = errValue.Convert(errorType)
			}
		}

		return []reflect.Value{tasksValue, errValue}
	})

	stubFactory := reflect.MakeFunc(factoryType, func(_ []reflect.Value) []reflect.Value {
		return []reflect.Value{queryFunc}
	})

	factoryValue.Set(stubFactory)
	return func() {
		factoryValue.Set(originalFactory)
	}
}
