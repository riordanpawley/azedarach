package protocol

import (
	"testing"

	"github.com/riordanpawley/azedarach/internal/naming"
)

func TestGlobalViewConsumerValid(t *testing.T) {
	tests := []struct {
		consumer GlobalViewConsumer
		valid    bool
	}{
		{GlobalViewConsumerBoard, true},
		{GlobalViewConsumerTmuxSelector, true},
		{GlobalViewConsumerSearch, true},
		{GlobalViewConsumerReview, true},
		{"global-board", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := tt.consumer.Valid(); got != tt.valid {
			t.Errorf("GlobalViewConsumer(%q).Valid() = %v, want %v", tt.consumer, got, tt.valid)
		}
	}
}

func TestGlobalViewScopeValidate(t *testing.T) {
	tests := []struct {
		name  string
		scope GlobalViewScope
		ok    bool
	}{
		{"default all", GlobalViewScope{}, true},
		{"all", GlobalViewScope{Kind: GlobalViewScopeAllProjects}, true},
		{"selected", GlobalViewScope{Kind: GlobalViewScopeSelectedProjects, ProjectIDs: []naming.ProjectID{"alpha"}}, true},
		{"selected empty", GlobalViewScope{Kind: GlobalViewScopeSelectedProjects}, false},
		{"current", GlobalViewScope{Kind: GlobalViewScopeCurrentProject, CurrentProjectID: "alpha"}, true},
		{"current empty", GlobalViewScope{Kind: GlobalViewScopeCurrentProject}, false},
		{"unknown", GlobalViewScope{Kind: "somewhere"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.scope.Validate() == nil; got != tt.ok {
				t.Fatalf("Validate() success = %v, want %v", got, tt.ok)
			}
		})
	}
}
