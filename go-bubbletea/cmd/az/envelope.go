package main

const (
	// AzCLIJSONSchemaVersion is the canonical schema version for top-level JSON output.
	AzCLIJSONSchemaVersion = "1.0"
)

// H1Error is the deterministic error payload shared across JSON failures.
type H1Error struct {
	Code        string         `json:"code"`
	Message     string         `json:"message"`
	Remediation string         `json:"remediation"`
	Details     map[string]any `json:"details,omitempty"`
}

// H1Success carries the inner success section of an H2 response.
type H1Success[T any] struct {
	OK     bool     `json:"ok"`
	Result T        `json:"result"`
	Error  *H1Error `json:"error"`
}

// H1Failure carries the inner failure section of an H2 response.
type H1Failure struct {
	OK     bool    `json:"ok"`
	Result any     `json:"result"`
	Error  H1Error `json:"error"`
}

// H2ProjectContext identifies the active project scope used for command execution.
type H2ProjectContext struct {
	ID              string `json:"id"`
	Path            string `json:"path"`
	CanonicalDBPath string `json:"canonicalDbPath"`
}

// H2Meta provides deterministic execution metadata.
type H2Meta struct {
	DurationMs int64  `json:"durationMs"`
	At         string `json:"at"`
}

// H2SuccessEnvelope is the canonical top-level success envelope for CLI JSON output.
type H2SuccessEnvelope[T any] struct {
	SchemaVersion string           `json:"schemaVersion"`
	Command       string           `json:"command"`
	CommandPath   []string         `json:"commandPath"`
	Project       H2ProjectContext `json:"project"`
	OK            bool             `json:"ok"`
	Result        T                `json:"result"`
	Error         *H1Error         `json:"error"`
	Meta          H2Meta           `json:"meta"`
}

// H2FailureEnvelope is the canonical top-level failure envelope for CLI JSON output.
type H2FailureEnvelope struct {
	SchemaVersion string           `json:"schemaVersion"`
	Command       string           `json:"command"`
	CommandPath   []string         `json:"commandPath"`
	Project       H2ProjectContext `json:"project"`
	OK            bool             `json:"ok"`
	Result        any              `json:"result"`
	Error         H1Error          `json:"error"`
	Meta          H2Meta           `json:"meta"`
}

// NewH1Error creates a deterministic H1 error payload.
func NewH1Error(code, message, remediation string, details map[string]any) H1Error {
	errorDetails := cloneDetails(details)

	return H1Error{
		Code:        code,
		Message:     message,
		Remediation: remediation,
		Details:     errorDetails,
	}
}

// NewH2SuccessEnvelope creates a deterministic H2 success envelope.
func NewH2SuccessEnvelope[T any](
	command string,
	commandPath []string,
	project H2ProjectContext,
	meta H2Meta,
	result T,
) H2SuccessEnvelope[T] {
	return H2SuccessEnvelope[T]{
		SchemaVersion: AzCLIJSONSchemaVersion,
		Command:       command,
		CommandPath:   cloneStrings(commandPath),
		Project:       project,
		OK:            true,
		Result:        result,
		Error:         nil,
		Meta:          meta,
	}
}

// NewH2FailureEnvelope creates a deterministic H2 failure envelope.
func NewH2FailureEnvelope(
	command string,
	commandPath []string,
	project H2ProjectContext,
	meta H2Meta,
	errPayload H1Error,
) H2FailureEnvelope {
	return H2FailureEnvelope{
		SchemaVersion: AzCLIJSONSchemaVersion,
		Command:       command,
		CommandPath:   cloneStrings(commandPath),
		Project:       project,
		OK:            false,
		Result:        nil,
		Error:         errPayload,
		Meta:          meta,
	}
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	result := make([]string, len(values))
	copy(result, values)
	return result
}

func cloneDetails(details map[string]any) map[string]any {
	if len(details) == 0 {
		return nil
	}

	copied := make(map[string]any, len(details))
	for key, value := range details {
		copied[key] = value
	}

	return copied
}
