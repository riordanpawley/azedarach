package notices

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/riordanpawley/azedarach/internal/daemon/publish"
	"github.com/riordanpawley/azedarach/internal/naming"
)

type Service struct {
	repo         Repository
	hub          *publish.Hub
	nextRevision func(string) uint64
	logger       *slog.Logger
}

type ServiceConfig struct {
	Repository   Repository
	Hub          *publish.Hub
	NextRevision func(string) uint64
	Logger       *slog.Logger
}

func NewService(cfg ServiceConfig) *Service {
	return &Service{
		repo:         cfg.Repository,
		hub:          cfg.Hub,
		nextRevision: cfg.NextRevision,
		logger:       cfg.Logger,
	}
}

func (s *Service) Close() error {
	if s == nil || s.repo == nil {
		return nil
	}
	return s.repo.Close()
}

func (s *Service) Upsert(ctx context.Context, candidate Candidate) (Record, bool, uint64, error) {
	if s == nil || s.repo == nil {
		return Record{}, false, 0, errors.New("notice repository unavailable")
	}
	record, created, err := s.repo.UpsertActive(ctx, candidate)
	if err != nil {
		return Record{}, false, 0, err
	}
	event := protocol.EventNoticeUpdated
	if created {
		event = protocol.EventNoticeCreated
	}
	rev := s.publish(ctx, event, record)
	return record, created, rev, nil
}

func (s *Service) Get(ctx context.Context, projectID, noticeID string) (Record, error) {
	if s == nil || s.repo == nil {
		return Record{}, errors.New("notice repository unavailable")
	}
	return s.repo.Get(ctx, projectID, noticeID)
}

func (s *Service) List(ctx context.Context, query Query) ([]Record, error) {
	if s == nil || s.repo == nil {
		return nil, errors.New("notice repository unavailable")
	}
	return s.repo.List(ctx, query)
}

func (s *Service) Update(ctx context.Context, params UpdateParams) (Record, bool, uint64, error) {
	if s == nil || s.repo == nil {
		return Record{}, false, 0, errors.New("notice repository unavailable")
	}
	record, changed, err := s.repo.Update(ctx, params)
	if err != nil {
		return Record{}, false, 0, err
	}
	if !changed {
		return record, false, 0, nil
	}
	rev := s.publish(ctx, protocol.EventNoticeUpdated, record)
	return record, true, rev, nil
}

func (s *Service) ExecuteAction(ctx context.Context, projectID, noticeID, actionID string, now time.Time) (Record, bool, uint64, error) {
	update := UpdateParams{
		ProjectID: projectID,
		NoticeID:  noticeID,
		Now:       now,
	}
	switch strings.TrimSpace(actionID) {
	case "mark_read":
		read := true
		update.Read = &read
	case "mark_unread":
		read := false
		update.Read = &read
	case "dismiss":
		update.State = StateDismissed
	case "resolve":
		update.State = StateResolved
	case "restore":
		update.State = StateActive
	default:
		return Record{}, false, 0, fmt.Errorf("%w: unsupported action_id %q", ErrInvalidNotice, actionID)
	}
	return s.Update(ctx, update)
}

func (s *Service) ExpireDue(ctx context.Context, query ExpireQuery) ([]Record, []uint64, error) {
	if s == nil || s.repo == nil {
		return nil, nil, errors.New("notice repository unavailable")
	}
	records, err := s.repo.ExpireDue(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	revisions := make([]uint64, 0, len(records))
	for _, record := range records {
		revisions = append(revisions, s.publish(ctx, protocol.EventNoticeExpired, record))
	}
	return records, revisions, nil
}

func (s *Service) DeleteExpired(ctx context.Context, query ExpireQuery) ([]Record, []uint64, error) {
	if s == nil || s.repo == nil {
		return nil, nil, errors.New("notice repository unavailable")
	}
	records, err := s.repo.DeleteExpired(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	revisions := make([]uint64, 0, len(records))
	for _, record := range records {
		revisions = append(revisions, s.publish(ctx, protocol.EventNoticeDeleted, record))
	}
	return records, revisions, nil
}

func (s *Service) publish(_ context.Context, event string, record Record) uint64 {
	if s == nil || s.hub == nil || s.nextRevision == nil {
		return 0
	}
	projectID := protocol.NormalizeProjectID(record.ProjectID)
	revision := s.nextRevision(projectID)
	protocolRecord := ToProtocol(record)
	body := protocol.NoticeEventBody{
		ProjectID: naming.ProjectID(projectID),
		Revision:  revision,
		NoticeID:  record.NoticeID,
		State:     record.State,
		UpdatedAt: record.UpdatedAt,
	}
	if event != protocol.EventNoticeDeleted {
		body.Notice = &protocolRecord
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("failed to marshal notice event body", "event", event, "notice_id", record.NoticeID, "error", err)
		}
		return revision
	}
	s.hub.Publish(protocol.EventEnvelope{
		ProtocolVersion: protocol.CurrentVersion,
		ProjectID:       naming.ProjectID(projectID),
		Revision:        revision,
		Event:           event,
		Kind:            protocol.EnvelopeKindEvent,
		EmittedAt:       time.Now().UTC(),
		Body:            encoded,
	})
	return revision
}

func RecordsToProtocol(records []Record) []protocol.NoticeRecord {
	out := make([]protocol.NoticeRecord, 0, len(records))
	for _, record := range records {
		out = append(out, ToProtocol(record))
	}
	return out
}
