// Package status holds the bounded, public operational state of the service.
package status

import (
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/acidghost/hetdns/internal/buildinfo"
	"github.com/acidghost/hetdns/internal/config"
)

// Snapshot is an immutable point-in-time status response.
type Snapshot struct {
	Build     buildinfo.Info `json:"build"`
	StartTime time.Time      `json:"start_time"`
	Uptime    string         `json:"uptime"`
	Overall   string         `json:"overall"`
	Ready     bool           `json:"ready"`
	Scheduler Scheduler      `json:"scheduler"`
	Sources   []Source       `json:"sources"`
	Records   []Record       `json:"records"`
	Events    []Event        `json:"events"`
}

// Scheduler describes cycle timing and lifecycle.
type Scheduler struct {
	State           string        `json:"state"`
	LastCycleStart  time.Time     `json:"last_cycle_start,omitzero"`
	LastCycleEnd    time.Time     `json:"last_cycle_end,omitzero"`
	LastCycleResult string        `json:"last_cycle_result,omitempty"`
	LastDuration    time.Duration `json:"last_duration_ns,omitempty"`
	NextCycle       time.Time     `json:"next_cycle,omitzero"`
	Cycle           uint64        `json:"cycle"`
}

// Source is safe source state. It intentionally omits URL and command details.
type Source struct {
	ID          string        `json:"id"`
	Family      string        `json:"family"`
	LastAttempt time.Time     `json:"last_attempt,omitzero"`
	LastSuccess time.Time     `json:"last_success,omitzero"`
	Address     string        `json:"address,omitempty"`
	Duration    time.Duration `json:"duration_ns,omitempty"`
	Error       string        `json:"error,omitempty"`
}

// Record is safe per-RRset state.
type Record struct {
	ID         string        `json:"id"`
	FQDN       string        `json:"fqdn"`
	Type       string        `json:"type"`
	Source     string        `json:"source"`
	Desired    string        `json:"desired,omitempty"`
	Observed   []string      `json:"observed,omitempty"`
	State      string        `json:"state"`
	LastCheck  time.Time     `json:"last_check,omitzero"`
	LastChange time.Time     `json:"last_change,omitzero"`
	NextRetry  time.Time     `json:"next_retry,omitzero"`
	Duration   time.Duration `json:"duration_ns,omitempty"`
	Error      string        `json:"error,omitempty"`
}

// Event is one bounded update or failure event.
type Event struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	ID      string    `json:"id,omitempty"`
	Message string    `json:"message"`
}

// Store is a concurrency-safe in-memory status store.
type Store struct {
	mu           sync.RWMutex
	build        buildinfo.Info
	started      time.Time
	ready        bool
	scheduler    Scheduler
	sources      map[string]Source
	records      map[string]Record
	sourceOrder  []string
	recordOrder  []string
	events       []Event
	historyLimit int
}

// New creates pending state for all configured sources and records.
func New(build buildinfo.Info, cfg *config.Config, historyLimit int) *Store {
	if historyLimit < 1 {
		historyLimit = 1
	}
	store := &Store{
		build:        build,
		started:      time.Now().UTC(),
		scheduler:    Scheduler{State: "initializing"},
		sources:      make(map[string]Source),
		records:      make(map[string]Record),
		historyLimit: historyLimit,
	}
	for id, sourceConfig := range cfg.Sources {
		store.sourceOrder = append(store.sourceOrder, id)
		store.sources[id] = Source{ID: id, Family: sourceConfig.Family}
	}
	for id, recordConfig := range cfg.Records {
		store.recordOrder = append(store.recordOrder, id)
		fqdn := recordConfig.Zone
		if recordConfig.Name != "@" {
			fqdn = recordConfig.Name + "." + recordConfig.Zone
		}
		store.records[id] = Record{
			ID:     id,
			FQDN:   fqdn,
			Type:   recordConfig.Type,
			Source: recordConfig.Source,
			State:  "pending",
		}
	}
	slices.Sort(store.sourceOrder)
	slices.Sort(store.recordOrder)
	return store
}

// SetReady updates initialization readiness and scheduler state.
func (s *Store) SetReady(ready bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ready = ready
	if ready {
		s.scheduler.State = "running"
	} else {
		s.scheduler.State = "stopping"
	}
}

// Ready reports current initialization readiness.
func (s *Store) Ready() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.ready
}

func (s *Store) CycleStarted(now time.Time) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scheduler.Cycle++
	s.scheduler.LastCycleStart = now.UTC()
	s.scheduler.LastCycleResult = "running"
	s.scheduler.NextCycle = time.Time{}
	return s.scheduler.Cycle
}

func (s *Store) CycleFinished(now time.Time, result string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scheduler.LastCycleEnd = now.UTC()
	s.scheduler.LastCycleResult = result
	s.scheduler.LastDuration = now.Sub(s.scheduler.LastCycleStart)
}

func (s *Store) SetNextCycle(next time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scheduler.NextCycle = next.UTC()
}

func (s *Store) SourceResult(id, address string, duration time.Duration, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.sources[id]
	now := time.Now().UTC()
	value.LastAttempt = now
	value.Duration = duration
	value.Error = safeError(err)
	if err == nil {
		value.LastSuccess = now
		value.Address = address
	}
	s.sources[id] = value
	if err != nil {
		s.addEventLocked(Event{Time: now, Kind: "source_error", ID: id, Message: value.Error})
	}
}

func (s *Store) RecordChecking(id, desired string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.records[id]
	value.State = "checking"
	value.Desired = desired
	value.Error = ""
	s.records[id] = value
}

func (s *Store) RecordResult(
	id string,
	state string,
	desired string,
	observed []string,
	duration time.Duration,
	changed bool,
	nextRetry time.Time,
	err error,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.records[id]
	now := time.Now().UTC()
	value.State = state
	value.Desired = desired
	value.Observed = append([]string(nil), observed...)
	value.Duration = duration
	value.LastCheck = now
	value.NextRetry = nextRetry.UTC()
	value.Error = safeError(err)
	if changed {
		value.LastChange = now
	}
	s.records[id] = value
	if changed {
		s.addEventLocked(Event{Time: now, Kind: state, ID: id, Message: "authoritative RRset changed"})
	} else if err != nil {
		s.addEventLocked(Event{Time: now, Kind: state, ID: id, Message: value.Error})
	}
}

func (s *Store) addEventLocked(event Event) {
	s.events = append(s.events, event)
	if excess := len(s.events) - s.historyLimit; excess > 0 {
		copy(s.events, s.events[excess:])
		s.events = s.events[:s.historyLimit]
	}
}

// Snapshot returns detached slices safe for callers to retain and encode.
func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot := Snapshot{
		Build:     s.build,
		StartTime: s.started,
		Uptime:    time.Since(s.started).Round(time.Second).String(),
		Ready:     s.ready,
		Scheduler: s.scheduler,
	}
	degraded := false
	pending := false
	for _, id := range s.sourceOrder {
		value := s.sources[id]
		snapshot.Sources = append(snapshot.Sources, value)
		if value.Error != "" {
			degraded = true
		}
		if value.LastAttempt.IsZero() {
			pending = true
		}
	}
	for _, id := range s.recordOrder {
		value := s.records[id]
		value.Observed = append([]string(nil), value.Observed...)
		snapshot.Records = append(snapshot.Records, value)
		switch value.State {
		case "source_error", "provider_error", "backoff":
			degraded = true
		case "pending", "checking":
			pending = true
		}
	}
	for _, event := range slices.Backward(s.events) {
		snapshot.Events = append(snapshot.Events, event)
	}
	switch {
	case degraded:
		snapshot.Overall = "degraded"
	case pending:
		snapshot.Overall = "pending"
	default:
		snapshot.Overall = "healthy"
	}
	return snapshot
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.Map(func(char rune) rune {
		if char < 0x20 || char == 0x7f {
			return ' '
		}
		return char
	}, err.Error())
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 256 {
		value = value[:256]
	}
	return value
}
