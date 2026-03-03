package attachments

import (
	"errors"
	"testing"

	"github.com/riordanpawley/azedarach/internal/testkit"
)

func TestServiceAddListDelete(t *testing.T) {
	t.Parallel()

	svc := NewService()

	err := svc.Add(Metadata{ID: "att-2", IssueID: "bd-10", Path: "./b.txt", Label: "b"})
	testkit.AssertNoError(t, err, "add second id should succeed")
	err = svc.Add(Metadata{ID: "att-1", IssueID: "bd-10", Path: "./a.txt", Label: "a"})
	testkit.AssertNoError(t, err, "add first id should succeed")

	attachments, err := svc.List("bd-10")
	testkit.AssertNoError(t, err, "list should succeed")
	testkit.AssertEqual(t, len(attachments), 2, "list should include two attachments")
	testkit.AssertEqual(t, attachments[0].ID, "att-1", "list should be deterministic by id")
	testkit.AssertEqual(t, attachments[1].ID, "att-2", "list should include second id")

	deleted, err := svc.Delete("att-1")
	testkit.AssertNoError(t, err, "delete should succeed")
	testkit.AssertTrue(t, deleted, "existing attachment should delete")

	attachments, err = svc.List("bd-10")
	testkit.AssertNoError(t, err, "list after delete should succeed")
	testkit.AssertEqual(t, len(attachments), 1, "one attachment should remain")
	testkit.AssertEqual(t, attachments[0].ID, "att-2", "remaining attachment should match")
}

func TestServiceRejectsInvalidAndDuplicateMetadata(t *testing.T) {
	t.Parallel()

	svc := NewService()

	err := svc.Add(Metadata{ID: "", IssueID: "bd-1", Path: "./x.txt"})
	if err == nil {
		t.Fatalf("expected invalid metadata to fail")
	}

	err = svc.Add(Metadata{ID: "att-1", IssueID: "bd-1", Path: "./x.txt"})
	testkit.AssertNoError(t, err, "first add should succeed")

	err = svc.Add(Metadata{ID: "att-1", IssueID: "bd-1", Path: "./x.txt"})
	if err == nil {
		t.Fatalf("expected duplicate id to fail")
	}
}

func TestServiceIntegrityChecks(t *testing.T) {
	t.Parallel()

	svc := NewService()

	err := svc.Add(Metadata{ID: "att-1", IssueID: "bd-1", Path: "./x.txt"})
	testkit.AssertNoError(t, err, "setup add should succeed")

	delete(svc.byID, "att-1")
	_, err = svc.List("bd-1")
	testkit.AssertTrue(t, errors.Is(err, ErrIntegrity), "list should detect broken index")
}
