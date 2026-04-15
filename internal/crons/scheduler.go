package crons

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/gorhill/cronexpr"
	protocol "github.com/gsd-build/protocol-go"
)

// StoreReader is the subset of Store needed by the scheduler.
type StoreReader interface {
	List() ([]LocalCron, error)
}

// DueHandler receives a due cron occurrence that should be claimed.
type DueHandler func(spec protocol.CronSpec, scheduledFor time.Time) error

// Scheduler periodically scans local cron definitions and emits due occurrences.
type Scheduler struct {
	store     StoreReader
	onDue     DueHandler
	tick      time.Duration
	log       *slog.Logger
	mu        sync.Mutex
	lastCheck time.Time
	lastDue   map[string]time.Time
}

func NewScheduler(store StoreReader, onDue DueHandler, tick time.Duration, log *slog.Logger) *Scheduler {
	if tick <= 0 {
		tick = 30 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{
		store:     store,
		onDue:     onDue,
		tick:      tick,
		log:       log,
		lastCheck: time.Now().UTC(),
		lastDue:   make(map[string]time.Time),
	}
}

func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.tickAt(now.UTC())
		}
	}
}

func (s *Scheduler) tickAt(now time.Time) {
	s.mu.Lock()
	from := s.lastCheck
	s.lastCheck = now
	s.mu.Unlock()

	locals, err := s.store.List()
	if err != nil {
		s.log.Warn("cron scheduler list failed", "err", err)
		return
	}

	for _, local := range locals {
		if !local.Spec.Enabled {
			continue
		}
		expr, err := cronexpr.Parse(local.Spec.CronExpression)
		if err != nil {
			s.log.Warn("cron scheduler parse failed", "cronJobId", local.Spec.ID, "err", err)
			continue
		}

		cursor := from
		for {
			next := expr.Next(cursor)
			if next.IsZero() || next.After(now) {
				break
			}
			if s.alreadyEmitted(local.Spec.ID, next) {
				cursor = next
				continue
			}
			if err := s.onDue(local.Spec, next); err != nil {
				s.log.Warn("cron scheduler due handler failed", "cronJobId", local.Spec.ID, "scheduledFor", next.Format(time.RFC3339Nano), "err", err)
			}
			s.recordDue(local.Spec.ID, next)
			cursor = next
		}
	}
}

func (s *Scheduler) alreadyEmitted(jobID string, scheduledFor time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	last, ok := s.lastDue[jobID]
	return ok && !scheduledFor.After(last)
}

func (s *Scheduler) recordDue(jobID string, scheduledFor time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastDue[jobID] = scheduledFor
}
