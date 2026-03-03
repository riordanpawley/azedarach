package probe

import (
	"errors"
	"testing"

	"github.com/riordanpawley/azedarach/internal/testkit"
)

func validPayload() Payload {
	return Payload{
		Mode:  "board",
		Focus: "column",
		Selection: SelectionState{
			Column: "in_progress",
			ItemID: "bd-42",
			Index:  1,
		},
		Overlay: OverlayState{
			Name:    "planning",
			Visible: true,
		},
		Operation: OperationSummary{
			Kind:   "plan",
			State:  "running",
			Active: true,
		},
	}
}

func TestMarshalAndUnmarshalRoundTrip(t *testing.T) {
	encoded, err := Marshal(validPayload())
	testkit.AssertNoError(t, err, "marshal should succeed")

	decoded, err := Unmarshal(encoded)
	testkit.AssertNoError(t, err, "unmarshal should succeed")

	testkit.AssertEqual(t, decoded.SchemaVersion, SchemaVersion, "schema version should be set")
	testkit.AssertEqual(t, decoded.Mode, "board", "mode should round-trip")
	testkit.AssertEqual(t, decoded.Focus, "column", "focus should round-trip")
	testkit.AssertEqual(t, decoded.Selection.ItemID, "bd-42", "selection should round-trip")
}

func TestUnmarshalRejectsWrongSchemaVersion(t *testing.T) {
	data := []byte(`{"schemaVersion":"2.0.0","mode":"board","focus":"column","selection":{"column":"open","index":0},"overlay":{"visible":false},"operation":{"active":false}}`)

	_, err := Unmarshal(data)
	testkit.AssertErrorIs(t, err, ErrInvalidSchemaVersion, "unexpected schema version should fail")
}

func TestMarshalRejectsInvalidPayload(t *testing.T) {
	payload := validPayload()
	payload.SchemaVersion = SchemaVersion
	payload.Selection.Index = -1

	_, err := Marshal(payload)
	testkit.AssertTrue(t, errors.Is(err, ErrNegativeSelection), "negative selection index should fail")
}
