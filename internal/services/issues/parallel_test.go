package issues

import "testing"

// issueStoreParallelSlots bounds concurrent SQLite-heavy tests below the
// process-wide testing -parallel limit. Two workers preserve useful overlap
// without turning isolated database fixtures into filesystem contention.
var issueStoreParallelSlots = make(chan struct{}, 2)

func parallelIssueStoreTest(t *testing.T) {
	t.Helper()
	t.Parallel()
	issueStoreParallelSlots <- struct{}{}
	t.Cleanup(func() { <-issueStoreParallelSlots })
}
