package attachments

import (
	"errors"
	"fmt"
	"sort"
)

var ErrIntegrity = errors.New("attachment metadata integrity failure")

type Metadata struct {
	ID      string
	IssueID string
	Path    string
	Label   string
}

type Service struct {
	byID    map[string]Metadata
	byIssue map[string][]string
}

func NewService() *Service {
	return &Service{
		byID:    map[string]Metadata{},
		byIssue: map[string][]string{},
	}
}

func (s *Service) Add(metadata Metadata) error {
	if metadata.ID == "" || metadata.IssueID == "" || metadata.Path == "" {
		return fmt.Errorf("invalid attachment metadata")
	}
	if _, exists := s.byID[metadata.ID]; exists {
		return fmt.Errorf("attachment already exists: %s", metadata.ID)
	}

	s.byID[metadata.ID] = metadata
	s.byIssue[metadata.IssueID] = append(s.byIssue[metadata.IssueID], metadata.ID)
	return nil
}

func (s *Service) List(issueID string) ([]Metadata, error) {
	ids := s.byIssue[issueID]
	if len(ids) == 0 {
		return nil, nil
	}

	out := make([]Metadata, 0, len(ids))
	for _, id := range ids {
		metadata, ok := s.byID[id]
		if !ok {
			return nil, fmt.Errorf("%w: missing attachment id %s", ErrIntegrity, id)
		}
		if metadata.IssueID != issueID {
			return nil, fmt.Errorf("%w: attachment %s indexed under wrong issue", ErrIntegrity, id)
		}
		out = append(out, metadata)
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})

	return out, nil
}

func (s *Service) Delete(attachmentID string) (bool, error) {
	metadata, ok := s.byID[attachmentID]
	if !ok {
		return false, nil
	}

	ids := s.byIssue[metadata.IssueID]
	index := -1
	for i := range ids {
		if ids[i] == attachmentID {
			index = i
			break
		}
	}
	if index == -1 {
		return false, fmt.Errorf("%w: attachment %s missing from issue index", ErrIntegrity, attachmentID)
	}

	delete(s.byID, attachmentID)
	updated := append(ids[:index], ids[index+1:]...)
	if len(updated) == 0 {
		delete(s.byIssue, metadata.IssueID)
	} else {
		s.byIssue[metadata.IssueID] = updated
	}

	return true, nil
}
