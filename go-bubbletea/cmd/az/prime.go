package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const (
	primeCommandName                = "prime"
	primeInvalidArgumentCode        = "invalid_argument"
	primeInvalidArgumentRemediation = "Run az prime --json without positional arguments"
	primeCommitTemplate             = "git commit -m \"AZE-123: <summary>\""
	primeBootstrapPrimeCommand      = "az prime"
	primeBootstrapIssueLookup       = "az show <issue-id>"
)

var primeCommandPath = []string{"az", "prime"}

type primePolicyChecklist struct {
	CloseWhenDone                 bool `json:"closeWhenDone"`
	CommitBeforeClose             bool `json:"commitBeforeClose"`
	RequireIssueIDInCommitMessage bool `json:"requireIssueIdInCommitMessage"`
}

type primeJSONData struct {
	QuickReference []string        `json:"quickReference"`
	Policies       map[string]bool `json:"policies"`
	CommitTemplate string          `json:"commitTemplate"`
	AgentBootstrap primeBootstrap  `json:"agentBootstrap"`
}

type primeBootstrap struct {
	PrimeCommand       string `json:"primeCommand"`
	IssueLookupCommand string `json:"issueLookupCommand"`
}

type primeOutputEnvelope struct {
	SchemaVersion string         `json:"schemaVersion"`
	Command       string         `json:"command"`
	CommandPath   []string       `json:"commandPath"`
	OK            bool           `json:"ok"`
	Data          *primeJSONData `json:"data,omitempty"`
	Error         *H1Error       `json:"error,omitempty"`
}

func handlePrimeCommand(args []string, stdout, stderr io.Writer) int {
	jsonMode := false
	invalidArgs := make([]string, 0)
	for _, arg := range args {
		if arg == "--json" {
			jsonMode = true
			continue
		}
		invalidArgs = append(invalidArgs, arg)
	}

	if len(invalidArgs) > 0 {
		invalidArgText := strings.Join(invalidArgs, " ")
		if jsonMode {
			errorPayload := BuildPrimeInvalidArgError(invalidArgText)
			envelope := primeOutputEnvelope{
				SchemaVersion: AzCLIJSONSchemaVersion,
				Command:       primeCommandName,
				CommandPath:   primeCommandPath,
				OK:            false,
				Error:         &errorPayload,
			}
			if err := writeJSON(stdout, envelope); err != nil {
				fmt.Fprintf(stderr, "Error: %v\n", err)
			}
			return 2
		}

		fmt.Fprintf(stderr, "unsupported positional args for az prime: %s\n", invalidArgText)
		fmt.Fprintf(stderr, "%s\n", primeInvalidArgumentRemediation)
		return 2
	}

	if jsonMode {
		envelope := primeOutputEnvelope{
			SchemaVersion: AzCLIJSONSchemaVersion,
			Command:       primeCommandName,
			CommandPath:   primeCommandPath,
			OK:            true,
			Data:          buildPrimeJSONData(),
		}
		if err := writeJSON(stdout, envelope); err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		return 0
	}

	printPrimeHuman(stdout)
	return 0
}

func buildPrimeJSONData() *primeJSONData {
	return &primeJSONData{
		QuickReference: primeQuickReference(),
		Policies: map[string]bool{
			"closeWhenDone":                 true,
			"commitBeforeClose":             true,
			"requireIssueIdInCommitMessage": true,
		},
		CommitTemplate: primeCommitTemplate,
		AgentBootstrap: primeBootstrap{
			PrimeCommand:       primeBootstrapPrimeCommand,
			IssueLookupCommand: primeBootstrapIssueLookup,
		},
	}
}

func primeQuickReference() []string {
	return []string{
		"az show <issue-id> --json",
		"az update <issue-id> ... --json",
		"az close <issue-id> --json",
		"az list --json",
	}
}

func printPrimeHuman(stdout io.Writer) {
	fmt.Fprintln(stdout, "Azedarach Session Primer")
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Bootstrap Contract:")
	fmt.Fprintln(stdout, "- Run az prime before substantive task execution.")
	fmt.Fprintln(stdout, "- Retrieve issue context with az show <issue-id>.")
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Quick Reference:")
	for _, command := range primeQuickReference() {
		fmt.Fprintf(stdout, "- %s\n", command)
	}
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Policies:")
	fmt.Fprintln(stdout, "- Close issues when done.")
	fmt.Fprintln(stdout, "- Commit before close operations.")
	fmt.Fprintln(stdout, "- Include issue ID in every commit message.")
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Commit Template:")
	fmt.Fprintf(stdout, "- %s\n", primeCommitTemplate)
}

func writeJSON(stdout io.Writer, payload any) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(payload)
}

func BuildPrimeInvalidArgError(argument string) H1Error {
	message := "az prime does not accept positional arguments"
	details := map[string]any{}
	if argument != "" {
		message = fmt.Sprintf("%s: %s", message, argument)
		details["argument"] = argument
	}

	return NewH1Error(
		primeInvalidArgumentCode,
		message,
		primeInvalidArgumentRemediation,
		details,
	)
}
