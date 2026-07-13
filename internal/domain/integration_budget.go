package domain

import "time"

// Integration validation and lifecycle budgets are deliberately layered. The
// repository merge gate owns the validation window, Git retains time to finish
// the merge and post-merge hooks after validation, and task close retains a
// final cleanup/status-write reserve after Git completes.
const (
	IntegrationValidationTimeout = 10 * time.Minute
	IntegrationTestBinaryTimeout = 8 * time.Minute
	IntegrationFinalizeReserve   = 5 * time.Minute
	IntegrationMergeTimeout      = IntegrationValidationTimeout + IntegrationFinalizeReserve
	IntegrationCloseReserve      = 5 * time.Minute
	IntegrationCloseTimeout      = IntegrationMergeTimeout + IntegrationCloseReserve
	IntegrationClientReserve     = 1 * time.Minute
	IntegrationClientTimeout     = IntegrationCloseTimeout + IntegrationClientReserve
)
