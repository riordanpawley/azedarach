package testtiming

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseGoProcessesRetainsOnlyValidationLoad(t *testing.T) {
	got := parseGoProcesses([]byte(" 10 1 /usr/bin/go\n 11 10 /tmp/go-build/pkg.test\n 12 1 /bin/zsh\n 13 10 /usr/local/go/pkg/tool/compile\n"))
	assert.Equal(t, []GoProcess{{PID: 10, Parent: 1, Command: "go"}, {PID: 11, Parent: 10, Command: "pkg.test"}, {PID: 13, Parent: 10, Command: "compile"}}, got)
}

func TestClassifyGoProcessesSeparatesCurrentTreeFromExternalLoad(t *testing.T) {
	all, external := classifyGoProcesses([]byte(" 1 0 /sbin/launchd\n 10 1 /usr/bin/go\n 20 10 /tmp/test-timing\n 21 20 /usr/bin/go\n 22 21 /tmp/go-build/pkg.test\n 30 1 /usr/bin/go\n 31 30 /usr/local/go/pkg/tool/compile\n"), 20)
	assert.Len(t, all, 5)
	assert.Equal(t, []GoProcess{{PID: 30, Parent: 1, Command: "go"}, {PID: 31, Parent: 30, Command: "compile"}}, external)
}
