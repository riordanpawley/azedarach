package daemonclient

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

const (
	auditEnvInvocationID = "AZEDARACH_AUDIT_INVOCATION_ID"
	auditEnvCommandShape = "AZEDARACH_AUDIT_COMMAND_SHAPE"
	auditEnvArgv         = "AZEDARACH_AUDIT_ARGV_JSON"
	auditEnvExecutable   = "AZEDARACH_AUDIT_EXECUTABLE"
	auditEnvPID          = "AZEDARACH_AUDIT_PID"
	auditEnvPPID         = "AZEDARACH_AUDIT_PPID"
	auditEnvCWD          = "AZEDARACH_AUDIT_CWD"
	auditEnvPWD          = "AZEDARACH_AUDIT_PWD"
	auditEnvActor        = "AZEDARACH_AUDIT_ACTOR"
	auditEnvUID          = "AZEDARACH_AUDIT_UID"
	auditEnvActiveIssue  = "AZEDARACH_AUDIT_ACTIVE_ISSUE"
)

var (
	auditCurrentPID  = os.Getpid
	auditCurrentPPID = os.Getppid
)

func populateClientAuditMetadata(meta *protocol.Metadata) {
	if meta == nil {
		return
	}
	if meta.ClientInvocationID == "" {
		meta.ClientInvocationID = strings.TrimSpace(os.Getenv(auditEnvInvocationID))
	}
	if meta.ClientCommandShape == "" {
		meta.ClientCommandShape = strings.TrimSpace(os.Getenv(auditEnvCommandShape))
	}
	if len(meta.ClientArgv) == 0 {
		meta.ClientArgv = readAuditArgvEnv()
	}
	if meta.ClientExecutable == "" {
		meta.ClientExecutable = strings.TrimSpace(os.Getenv(auditEnvExecutable))
	}
	if meta.ClientPID == 0 {
		meta.ClientPID = readAuditIntEnv(auditEnvPID)
	}
	if meta.ClientPPID == 0 {
		meta.ClientPPID = readAuditIntEnv(auditEnvPPID)
	}
	if meta.ClientCWD == "" {
		meta.ClientCWD = strings.TrimSpace(os.Getenv(auditEnvCWD))
	}
	if meta.ClientPWD == "" {
		meta.ClientPWD = strings.TrimSpace(os.Getenv(auditEnvPWD))
	}
	if meta.ClientActor == "" {
		meta.ClientActor = strings.TrimSpace(os.Getenv(auditEnvActor))
	}
	if meta.ClientUID == "" {
		meta.ClientUID = strings.TrimSpace(os.Getenv(auditEnvUID))
	}
	if meta.ClientActiveIssue == "" {
		meta.ClientActiveIssue = strings.TrimSpace(os.Getenv(auditEnvActiveIssue))
	}
	if meta.ClientPID == 0 {
		meta.ClientPID = auditCurrentPID()
	}
	if meta.ClientPPID == 0 || auditCommandShapeLooksWatch(meta.ClientCommandShape) {
		meta.ClientPPID = auditCurrentPPID()
	}
}

func readAuditArgvEnv() []string {
	raw := strings.TrimSpace(os.Getenv(auditEnvArgv))
	if raw == "" {
		return nil
	}
	var argv []string
	if err := json.Unmarshal([]byte(raw), &argv); err != nil {
		return nil
	}
	return argv
}

func readAuditIntEnv(name string) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil {
		return 0
	}
	return value
}

func auditCommandShapeLooksWatch(shape string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(shape)), " watch")
}
