// Package telemetry configures the OpenTelemetry metrics SDK and OTLP/HTTP exporter.
package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/acidghost/hetdns/internal/buildinfo"
	"github.com/acidghost/hetdns/internal/status"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

// Metrics contains the service's bounded-cardinality instruments.
type Metrics struct {
	enabled        bool
	provider       *sdkmetric.MeterProvider
	cycles         metric.Int64Counter
	cycleDuration  metric.Float64Histogram
	sourceFetches  metric.Int64Counter
	sourceDuration metric.Float64Histogram
	recordChecks   metric.Int64Counter
	recordDuration metric.Float64Histogram
	mu             sync.RWMutex
	lastSuccess    map[string]int64
}

// New uses standard OTEL_* environment configuration. Without an endpoint it is a no-op.
func New(
	ctx context.Context,
	build buildinfo.Info,
	store *status.Store,
	logger *slog.Logger,
) (*Metrics, error) {
	metrics := &Metrics{lastSuccess: make(map[string]int64)}
	if logger == nil {
		logger = slog.Default()
	}
	if sdkDisabled() || !endpointConfigured() {
		return metrics, nil
	}

	protocol := firstNonEmpty(
		os.Getenv("OTEL_EXPORTER_OTLP_METRICS_PROTOCOL"),
		os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"),
	)
	if protocol != "" && protocol != "http/protobuf" {
		return nil, errors.New("OTLP metrics protocol must be http/protobuf")
	}
	exporter, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return nil, errors.New("initialize OTLP/HTTP metrics exporter")
	}

	readerOptions := []sdkmetric.PeriodicReaderOption{}
	if duration, ok := millisecondsEnvironment("OTEL_METRIC_EXPORT_INTERVAL"); ok {
		readerOptions = append(readerOptions, sdkmetric.WithInterval(duration))
	}
	if duration, ok := millisecondsEnvironment("OTEL_METRIC_EXPORT_TIMEOUT"); ok {
		readerOptions = append(readerOptions, sdkmetric.WithTimeout(duration))
	}
	reader := sdkmetric.NewPeriodicReader(exporter, readerOptions...)

	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "hetdns"
	}
	res, err := resource.New(
		ctx,
		resource.WithFromEnv(),
		resource.WithAttributes(
			attribute.String("service.name", serviceName),
			attribute.String("service.version", build.Version),
			attribute.String("vcs.revision", build.Commit),
		),
	)
	if err != nil {
		return nil, errors.New("initialize telemetry resource")
	}
	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(provider)
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) {
		logger.Warn("telemetry export failed", "component", "telemetry")
	}))
	meter := provider.Meter("github.com/acidghost/hetdns")
	metrics.provider = provider
	metrics.enabled = true

	metrics.cycles, err = meter.Int64Counter(
		"hetdns.reconcile.cycles",
		metric.WithDescription("Completed reconciliation cycles"),
	)
	if err != nil {
		return nil, err
	}
	metrics.cycleDuration, err = meter.Float64Histogram(
		"hetdns.reconcile.duration",
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}

	metrics.sourceFetches, err = meter.Int64Counter("hetdns.source.fetches")
	if err != nil {
		return nil, err
	}

	metrics.sourceDuration, err = meter.Float64Histogram(
		"hetdns.source.fetch.duration",
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}

	metrics.recordChecks, err = meter.Int64Counter("hetdns.record.checks")
	if err != nil {
		return nil, err
	}

	metrics.recordDuration, err = meter.Float64Histogram(
		"hetdns.record.check.duration",
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}

	lastSuccessGauge, err := meter.Int64ObservableGauge(
		"hetdns.record.last_success",
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}
	readyGauge, err := meter.Int64ObservableGauge("hetdns.scheduler.ready")
	if err != nil {
		return nil, err
	}

	_, err = meter.RegisterCallback(func(_ context.Context, observer metric.Observer) error {
		metrics.mu.RLock()
		for id, timestamp := range metrics.lastSuccess {
			observer.ObserveInt64(
				lastSuccessGauge,
				timestamp,
				metric.WithAttributes(attribute.String("record.id", id)),
			)
		}
		metrics.mu.RUnlock()

		ready := int64(0)
		if store.Ready() {
			ready = 1
		}
		observer.ObserveInt64(readyGauge, ready)
		return nil
	}, lastSuccessGauge, readyGauge)
	if err != nil {
		return nil, err
	}

	return metrics, nil
}

func (m *Metrics) Cycle(ctx context.Context, result string, duration time.Duration) {
	if !m.enabled {
		return
	}
	attrs := metric.WithAttributes(attribute.String("result", result))
	m.cycles.Add(ctx, 1, attrs)
	m.cycleDuration.Record(ctx, duration.Seconds(), attrs)
}

func (m *Metrics) Source(ctx context.Context, id, result string, duration time.Duration) {
	if !m.enabled {
		return
	}
	attrs := metric.WithAttributes(
		attribute.String("source.id", id),
		attribute.String("result", result),
	)
	m.sourceFetches.Add(ctx, 1, attrs)
	m.sourceDuration.Record(ctx, duration.Seconds(), attrs)
}

func (m *Metrics) Record(ctx context.Context, id, result string, duration time.Duration) {
	if !m.enabled {
		return
	}
	attrs := metric.WithAttributes(
		attribute.String("record.id", id),
		attribute.String("result", result),
	)
	m.recordChecks.Add(ctx, 1, attrs)
	m.recordDuration.Record(ctx, duration.Seconds(), attrs)
}

func (m *Metrics) RecordSuccess(id string, at time.Time) {
	m.mu.Lock()
	m.lastSuccess[id] = at.Unix()
	m.mu.Unlock()
}

// Shutdown performs a bounded flush and exporter shutdown.
func (m *Metrics) Shutdown(ctx context.Context) error {
	if m.provider == nil {
		return nil
	}

	flushErr := m.provider.ForceFlush(ctx)
	shutdownErr := m.provider.Shutdown(ctx)
	if flushErr != nil || shutdownErr != nil {
		return errors.New("flush or shutdown telemetry")
	}
	return nil
}

func endpointConfigured() bool {
	metricsEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT")
	sharedEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")

	return metricsEndpoint != "" || sharedEndpoint != ""
}
func sdkDisabled() bool {
	value, _ := strconv.ParseBool(os.Getenv("OTEL_SDK_DISABLED"))
	return value
}

func millisecondsEnvironment(key string) (time.Duration, bool) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return 0, false
	}
	milliseconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || milliseconds <= 0 {
		return 0, false
	}
	return time.Duration(milliseconds) * time.Millisecond, true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
