package domain

import "testing"

func TestIntegrationBudgetLayersPreserveLifecycleReserves(t *testing.T) {
	if IntegrationTestBinaryTimeout >= IntegrationValidationTimeout {
		t.Fatalf("test binary timeout = %v, want less than wall validation timeout %v", IntegrationTestBinaryTimeout, IntegrationValidationTimeout)
	}
	if IntegrationMergeTimeout != IntegrationValidationTimeout+IntegrationFinalizeReserve {
		t.Fatalf("merge timeout = %v, want validation %v + finalization reserve %v", IntegrationMergeTimeout, IntegrationValidationTimeout, IntegrationFinalizeReserve)
	}
	if IntegrationCloseTimeout != IntegrationMergeTimeout+IntegrationCloseReserve {
		t.Fatalf("close timeout = %v, want merge %v + cleanup reserve %v", IntegrationCloseTimeout, IntegrationMergeTimeout, IntegrationCloseReserve)
	}
	if IntegrationClientTimeout != IntegrationCloseTimeout+IntegrationClientReserve {
		t.Fatalf("client timeout = %v, want daemon close %v + transport reserve %v", IntegrationClientTimeout, IntegrationCloseTimeout, IntegrationClientReserve)
	}
}
