// Package reconcile performs complete, non-overlapping reconciliation cycles.
package reconcile

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/acidghost/hetdns/internal/config"
	"github.com/acidghost/hetdns/internal/hetzner"
	"github.com/acidghost/hetdns/internal/source"
	"github.com/acidghost/hetdns/internal/status"
)

// DNSProvider is the provider boundary used by reconciliation tests.
type DNSProvider interface {
	Reconcile(context.Context, hetzner.Record, netip.Addr) (hetzner.Result, error)
}

// Metrics receives bounded reconciliation measurements.
type Metrics interface {
	Cycle(context.Context, string, time.Duration)
	Source(context.Context, string, string, time.Duration)
	Record(context.Context, string, string, time.Duration)
	RecordSuccess(string, time.Time)
}

type noopMetrics struct{}

func (noopMetrics) Cycle(context.Context, string, time.Duration)          {}
func (noopMetrics) Source(context.Context, string, string, time.Duration) {}
func (noopMetrics) Record(context.Context, string, string, time.Duration) {}
func (noopMetrics) RecordSuccess(string, time.Time)                       {}

// Reconciler performs one cycle at a time. Scheduler is responsible for serialization.
type Reconciler struct {
	cfg      *config.Config
	sources  []source.Source
	provider DNSProvider
	status   *status.Store
	metrics  Metrics
	logger   *slog.Logger
	mu       sync.Mutex
	backoff  map[string]retryState
}

type retryState struct {
	failures int
	next     time.Time
}

type sourceResult struct {
	address  netip.Addr
	err      error
	duration time.Duration
}

// New constructs a reconciler.
func New(
	cfg *config.Config,
	sources []source.Source,
	provider DNSProvider,
	store *status.Store,
	metrics Metrics,
	logger *slog.Logger,
) *Reconciler {
	if metrics == nil {
		metrics = noopMetrics{}
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &Reconciler{
		cfg:      cfg,
		sources:  append([]source.Source(nil), sources...),
		provider: provider,
		status:   store,
		metrics:  metrics,
		logger:   logger,
		backoff:  make(map[string]retryState),
	}
}

// Run performs one full cycle. It returns success or degraded.
func (r *Reconciler) Run(ctx context.Context) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	started := time.Now()
	cycle := r.status.CycleStarted(started)
	results := r.fetchSources(ctx)
	degraded := false

	ids := make([]string, 0, len(r.cfg.Records))
	for id := range r.cfg.Records {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		recordConfig := r.cfg.Records[id]
		sourceValue := results[recordConfig.Source]
		if sourceValue.err != nil {
			degraded = true
			r.status.RecordResult(
				id,
				"source_error",
				"",
				nil,
				0,
				false,
				time.Time{},
				errors.New("configured address source failed"),
			)
			r.metrics.Record(ctx, id, "source_error", 0)
			continue
		}

		desired := sourceValue.address.String()
		now := time.Now()
		if retry := r.backoff[id]; now.Before(retry.next) {
			degraded = true
			r.status.RecordResult(
				id,
				"backoff",
				desired,
				nil,
				0,
				false,
				retry.next,
				nil,
			)
			r.metrics.Record(ctx, id, "backoff", 0)
			continue
		}

		r.status.RecordChecking(id, desired)

		providerRecord := hetzner.Record{
			ID:         id,
			Zone:       recordConfig.Zone,
			Name:       recordConfig.Name,
			Type:       recordConfig.Type,
			TTL:        recordConfig.TTL,
			Create:     recordConfig.MayCreate(),
			ReplaceAll: recordConfig.ReplaceAll,
		}
		recordCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		recordStart := time.Now()
		result, err := r.provider.Reconcile(
			recordCtx,
			providerRecord,
			sourceValue.address,
		)
		cancel()
		duration := time.Since(recordStart)
		if err != nil {
			degraded = true
			next := r.failRecord(id, err)
			r.status.RecordResult(
				id,
				"provider_error",
				desired,
				result.Observed,
				duration,
				false,
				next,
				err,
			)
			r.metrics.Record(ctx, id, "provider_error", duration)
			r.logger.Warn(
				"record reconciliation failed",
				"component", "reconcile",
				"cycle", cycle,
				"record", id,
				"result", "provider_error",
				"duration", duration,
				"error", err,
			)
			continue
		}

		delete(r.backoff, id)
		state := string(result.State)
		changed := result.State == hetzner.Created || result.State == hetzner.Updated
		if changed {
			state = "updated"
			r.logger.Info(
				"authoritative RRset changed",
				"component", "reconcile",
				"cycle", cycle,
				"record", id,
				"result", result.State,
				"duration", duration,
			)
		} else {
			r.logger.Debug(
				"authoritative RRset unchanged",
				"component", "reconcile",
				"cycle", cycle,
				"record", id,
				"result", state,
				"duration", duration,
			)
		}

		r.status.RecordResult(
			id,
			state,
			desired,
			result.Observed,
			duration,
			changed,
			time.Time{},
			nil,
		)
		r.metrics.Record(ctx, id, state, duration)
		r.metrics.RecordSuccess(id, time.Now())
	}

	outcome := "success"
	if degraded {
		outcome = "degraded"
	}
	duration := time.Since(started)
	r.status.CycleFinished(time.Now(), outcome)
	r.metrics.Cycle(ctx, outcome, duration)
	r.logger.Info(
		"reconciliation cycle complete",
		"component", "reconcile",
		"cycle", cycle,
		"result", outcome,
		"duration", duration,
	)
	return outcome
}

func (r *Reconciler) fetchSources(ctx context.Context) map[string]sourceResult {
	results := make(map[string]sourceResult, len(r.sources))
	var resultMu sync.Mutex
	jobs := make(chan source.Source)
	var workers sync.WaitGroup
	workerCount := min(4, len(r.sources))
	for range workerCount {
		workers.Go(func() {
			for item := range jobs {
				started := time.Now()
				address, err := item.Fetch(ctx)
				duration := time.Since(started)
				resultMu.Lock()
				results[item.ID()] = sourceResult{address: address, err: err, duration: duration}
				resultMu.Unlock()
				addressText := ""
				outcome := "success"
				if err != nil {
					outcome = "error"
				} else {
					addressText = address.String()
				}
				r.status.SourceResult(item.ID(), addressText, duration, err)
				r.metrics.Source(ctx, item.ID(), outcome, duration)
			}
		})
	}
	for _, item := range r.sources {
		jobs <- item
	}
	close(jobs)
	workers.Wait()
	return results
}

func (r *Reconciler) failRecord(id string, err error) time.Time {
	state := r.backoff[id]
	state.failures++
	delay := time.Minute * time.Duration(1<<min(state.failures-1, 6))
	var apiError *hetzner.APIError
	if errors.As(err, &apiError) && apiError.RetryAfter > delay {
		delay = apiError.RetryAfter
	}
	if delay > time.Hour {
		delay = time.Hour
	}
	state.next = time.Now().Add(delay)
	r.backoff[id] = state
	return state.next
}
