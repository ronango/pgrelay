// Package otel wires the OpenTelemetry SDK into pgrelay.
//
// Setup configures a TracerProvider with an OTLP/HTTP exporter (when an
// endpoint is provided) and registers the W3C Trace Context + Baggage
// propagators globally. The returned shutdown function drains the batch
// span processor; callers are responsible for invoking it on exit.
package otel

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	// semconv is pinned per OTel guidance: bumping requires a deliberate
	// review because attribute names can change across versions.
	semconv "go.opentelemetry.io/otel/semconv/v1.27.0"
)

// Config holds the OTel SDK setup parameters typically populated from
// internal/config.Config.
type Config struct {
	ServiceName    string
	ServiceVersion string

	// OTLPEndpoint empty disables the OTLP exporter; the propagator and
	// TracerProvider are still installed (sampling decisions still apply,
	// spans just have nowhere to go). This makes local dev painless.
	OTLPEndpoint string
	OTLPHeaders  map[string]string

	// TracesSampler matches the OTEL_TRACES_SAMPLER spec values that the
	// SDK auto-config understands. TracesSamplerArg is the ratio when the
	// sampler is *traceidratio*.
	TracesSampler    string
	TracesSamplerArg string
}

// validTracesSamplers and samplerNeedsRatio mirror internal/config; we keep
// them in this package so otel.Config can be constructed independently
// (e.g. in tests) without round-tripping through env.
var validTracesSamplers = []string{
	"always_on",
	"always_off",
	"traceidratio",
	"parentbased_always_on",
	"parentbased_always_off",
	"parentbased_traceidratio",
}

func samplerNeedsRatio(name string) bool {
	return name == "traceidratio" || name == "parentbased_traceidratio"
}

// Validate checks Config invariants for callers that build Config in code
// rather than going through internal/config.Load.
func (c Config) Validate() error {
	if c.ServiceName == "" {
		return fmt.Errorf("ServiceName: required")
	}
	if c.OTLPEndpoint != "" {
		u, err := url.Parse(c.OTLPEndpoint)
		if err != nil {
			return fmt.Errorf("OTLPEndpoint: %w", err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("OTLPEndpoint: scheme must be http or https, got %q", u.Scheme)
		}
		if u.Host == "" {
			return fmt.Errorf("OTLPEndpoint: missing host in %q", c.OTLPEndpoint)
		}
	}
	if !slices.Contains(validTracesSamplers, c.TracesSampler) {
		return fmt.Errorf("TracesSampler: unknown value %q", c.TracesSampler)
	}
	if samplerNeedsRatio(c.TracesSampler) {
		if _, err := parseRatio(c.TracesSamplerArg); err != nil {
			return fmt.Errorf("TracesSamplerArg: %w", err)
		}
	} else if c.TracesSamplerArg != "" {
		return fmt.Errorf("TracesSamplerArg: must be empty when sampler is %q", c.TracesSampler)
	}
	return nil
}

// Setup installs the global TracerProvider and TextMapPropagator, then
// returns a shutdown func that drains pending spans. Always installs the
// W3C Trace Context + Baggage propagators, even when the OTLP exporter
// is disabled — propagation is the foundation outbox tracing relies on.
//
// All-or-nothing: any error returns nil shutdown and leaves the global
// state untouched. The propagator and TracerProvider are installed only
// after every fallible step succeeds.
func Setup(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate otel config: %w", err)
	}

	sampler, err := buildSampler(cfg.TracesSampler, cfg.TracesSamplerArg)
	if err != nil {
		return nil, fmt.Errorf("build sampler: %w", err)
	}

	// Use TelemetrySDK to populate telemetry.sdk.* attrs without forcing a
	// schema URL on the resource — the SDK's own schema can drift from
	// our pinned semconv version, and an explicit WithSchemaURL conflicts.
	res, err := resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("build resource: %w", err)
	}

	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	}

	if cfg.OTLPEndpoint != "" {
		exporter, err := newOTLPExporter(ctx, cfg.OTLPEndpoint, cfg.OTLPHeaders)
		if err != nil {
			return nil, fmt.Errorf("new otlp exporter: %w", err)
		}
		opts = append(opts, sdktrace.WithBatcher(exporter))
	}

	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tp.Shutdown, nil
}

// newOTLPExporter parses cfg.OTLPEndpoint into host:port + insecure flag
// and returns a configured otlptracehttp exporter. http scheme implies
// WithInsecure(); https uses TLS via the system trust store.
func newOTLPExporter(ctx context.Context, endpoint string, headers map[string]string) (sdktrace.SpanExporter, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse endpoint: %w", err)
	}

	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(u.Host),
	}
	if u.Scheme == "http" {
		opts = append(opts, otlptracehttp.WithInsecure())
	}
	// Forward path when given (collector docs commonly publish endpoints
	// like "http://collector:4318/v1/traces"); otherwise SDK defaults to
	// the spec-canonical /v1/traces.
	if u.Path != "" && u.Path != "/" {
		opts = append(opts, otlptracehttp.WithURLPath(u.Path))
	}
	if len(headers) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(headers))
	}
	return otlptracehttp.New(ctx, opts...)
}

func buildSampler(name, arg string) (sdktrace.Sampler, error) {
	switch name {
	case "always_on":
		return sdktrace.AlwaysSample(), nil
	case "always_off":
		return sdktrace.NeverSample(), nil
	case "traceidratio":
		ratio, err := parseRatio(arg)
		if err != nil {
			return nil, err
		}
		return sdktrace.TraceIDRatioBased(ratio), nil
	case "parentbased_always_on":
		return sdktrace.ParentBased(sdktrace.AlwaysSample()), nil
	case "parentbased_always_off":
		return sdktrace.ParentBased(sdktrace.NeverSample()), nil
	case "parentbased_traceidratio":
		ratio, err := parseRatio(arg)
		if err != nil {
			return nil, err
		}
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio)), nil
	default:
		return nil, fmt.Errorf("unknown sampler %q", name)
	}
}

func parseRatio(arg string) (float64, error) {
	if arg == "" {
		return 0, fmt.Errorf("ratio required")
	}
	r, err := strconv.ParseFloat(arg, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid ratio %q: %w", arg, err)
	}
	if r < 0 || r > 1 {
		return 0, fmt.Errorf("ratio %v out of [0,1]", r)
	}
	return r, nil
}
