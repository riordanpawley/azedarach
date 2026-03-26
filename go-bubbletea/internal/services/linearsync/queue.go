package linearsync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalidConfig  = errors.New("invalid linearsync queue config")
	ErrInvalidRequest = errors.New("invalid linearsync request")
)

type Ticker interface {
	C() <-chan time.Time
	Stop()
}

type TickerFactory func(time.Duration) Ticker

type Config struct {
	RatePerSecond int
	Burst         int
	Now           func() time.Time
	NewTicker     TickerFactory
}

type Request struct {
	ID        string
	ProjectID string
	IssueID   string
	Kind      string
	DedupeKey string
	Work      func(context.Context) error
}

type Submission struct {
	RequestID string
	Deduped   bool
	Done      <-chan Result
}

type Result struct {
	RequestID  string
	ProjectID  string
	IssueID    string
	Kind       string
	DedupeKey  string
	StartedAt  time.Time
	FinishedAt time.Time
	Err        error
}

type Queue struct {
	cfg    Config
	now    func() time.Time
	newTkr TickerFactory
	period time.Duration

	mu      sync.Mutex
	tokens  int
	pending []*job
	claims  map[string]*job
	wake    chan struct{}
}

type job struct {
	request Request
	waiters []chan Result
}

func New(cfg Config) (*Queue, error) {
	if cfg.RatePerSecond <= 0 {
		return nil, fmt.Errorf("%w: rate_per_second must be > 0", ErrInvalidConfig)
	}
	if cfg.Burst <= 0 {
		return nil, fmt.Errorf("%w: burst must be > 0", ErrInvalidConfig)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	newTkr := cfg.NewTicker
	if newTkr == nil {
		newTkr = func(d time.Duration) Ticker {
			return realTicker{time.NewTicker(d)}
		}
	}

	period := time.Second / time.Duration(cfg.RatePerSecond)
	if period <= 0 {
		period = time.Nanosecond
	}

	return &Queue{
		cfg:    cfg,
		now:    now,
		newTkr: newTkr,
		period: period,
		tokens: cfg.Burst,
		claims: make(map[string]*job),
		wake:   make(chan struct{}, 1),
	}, nil
}

func (q *Queue) Run(ctx context.Context) error {
	ticker := q.newTkr(q.period)
	defer ticker.Stop()

	q.drain(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C():
			q.refill()
			q.drain(ctx)
		case <-q.wake:
			q.drain(ctx)
		}
	}
}

func (q *Queue) Submit(ctx context.Context, req Request) (Submission, error) {
	if err := ctx.Err(); err != nil {
		return Submission{}, err
	}
	normalized, err := normalizeRequest(req)
	if err != nil {
		return Submission{}, err
	}

	done := make(chan Result, 1)
	key := normalized.dedupeMapKey()

	q.mu.Lock()
	defer q.mu.Unlock()

	if key != "" {
		if existing, ok := q.claims[key]; ok {
			existing.waiters = append(existing.waiters, done)
			return Submission{
				RequestID: existing.request.ID,
				Deduped:   true,
				Done:      done,
			}, nil
		}
	}

	j := &job{
		request: normalized,
		waiters: []chan Result{done},
	}
	q.pending = append(q.pending, j)
	if key != "" {
		q.claims[key] = j
	}
	q.signalLocked()

	return Submission{
		RequestID: normalized.ID,
		Deduped:   false,
		Done:      done,
	}, nil
}

func (q *Queue) refill() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.tokens < q.cfg.Burst {
		q.tokens++
	}
}

func (q *Queue) drain(ctx context.Context) {
	for {
		q.mu.Lock()
		if q.tokens <= 0 || len(q.pending) == 0 {
			q.mu.Unlock()
			return
		}
		j := q.pending[0]
		q.pending = q.pending[1:]
		q.tokens--
		q.mu.Unlock()

		go q.execute(ctx, j)
	}
}

func (q *Queue) execute(ctx context.Context, j *job) {
	startedAt := q.now().UTC()
	err := j.request.Work(ctx)
	finishedAt := q.now().UTC()

	res := Result{
		RequestID:  j.request.ID,
		ProjectID:  j.request.ProjectID,
		IssueID:    j.request.IssueID,
		Kind:       j.request.Kind,
		DedupeKey:  j.request.DedupeKey,
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
		Err:        err,
	}

	q.broadcast(j, res)
}

func (q *Queue) broadcast(j *job, res Result) {
	q.mu.Lock()
	defer q.mu.Unlock()

	key := j.request.dedupeMapKey()
	if key != "" {
		if current, ok := q.claims[key]; ok && current == j {
			delete(q.claims, key)
		}
	}

	for _, waiter := range j.waiters {
		waiter <- res
		close(waiter)
	}
}

func (q *Queue) signalLocked() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func normalizeRequest(req Request) (Request, error) {
	req.ID = strings.TrimSpace(req.ID)
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.IssueID = strings.TrimSpace(req.IssueID)
	req.Kind = strings.TrimSpace(req.Kind)
	req.DedupeKey = strings.TrimSpace(req.DedupeKey)
	if req.ID == "" {
		return Request{}, fmt.Errorf("%w: missing request id", ErrInvalidRequest)
	}
	if req.ProjectID == "" {
		return Request{}, fmt.Errorf("%w: missing project id", ErrInvalidRequest)
	}
	if req.IssueID == "" {
		return Request{}, fmt.Errorf("%w: missing issue id", ErrInvalidRequest)
	}
	if req.Kind == "" {
		return Request{}, fmt.Errorf("%w: missing kind", ErrInvalidRequest)
	}
	if req.Work == nil {
		return Request{}, fmt.Errorf("%w: missing work func", ErrInvalidRequest)
	}
	return req, nil
}

func (r Request) dedupeMapKey() string {
	if strings.TrimSpace(r.DedupeKey) == "" {
		return ""
	}
	return r.ProjectID + "::" + r.DedupeKey
}

type realTicker struct {
	*time.Ticker
}

func (t realTicker) C() <-chan time.Time {
	return t.Ticker.C
}
