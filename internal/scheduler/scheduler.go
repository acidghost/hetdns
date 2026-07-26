// Package scheduler runs immediate and delayed non-overlapping cycles.
package scheduler

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/acidghost/hetdns/internal/status"
)

// Runner performs one complete cycle.
type Runner interface{ Run(context.Context) string }

// Scheduler waits until each cycle finishes before scheduling the next one.
type Scheduler struct {
	interval time.Duration
	runner   Runner
	status   *status.Store
}

func New(interval time.Duration, runner Runner, store *status.Store) *Scheduler {
	return &Scheduler{interval: interval, runner: runner, status: store}
}

// Run starts immediately and blocks until cancellation.
func (s *Scheduler) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.runner.Run(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		delay := s.interval + s.jitter()
		next := time.Now().Add(delay)
		s.status.SetNextCycle(next)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Scheduler) jitter() time.Duration {
	bound := s.interval / 20 // bounded ±5%; an interval remains safely above its configured minimum.
	if bound <= 0 {
		return 0
	}
	// This random value is scheduling jitter, not a security primitive.
	return time.Duration(rand.Int64N(int64(bound)*2+1)) - bound // #nosec G404
}
