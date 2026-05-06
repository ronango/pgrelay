package otel

import (
	"context"
	"strings"
	"testing"

	otelapi "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func validBaseline() Config {
	return Config{
		ServiceName:    "pgrelay",
		ServiceVersion: "0.0.0-test",
		OTLPEndpoint:   "", // disabled — fastest path for tests
		TracesSampler:  "always_on",
	}
}

func TestConfig_ValidateAccepts(t *testing.T) {
	if err := validBaseline().Validate(); err != nil {
		t.Errorf("baseline: %v", err)
	}
}

func TestConfig_ValidateRejects(t *testing.T) {
	cases := []struct {
		name    string
		mut     func(*Config)
		wantSub string
	}{
		{"empty_service_name", func(c *Config) { c.ServiceName = "" }, "ServiceName"},
		{"bad_endpoint_scheme", func(c *Config) { c.OTLPEndpoint = "ftp://x" }, "scheme"},
		{"endpoint_no_host", func(c *Config) { c.OTLPEndpoint = "http://" }, "host"},
		{"unknown_sampler", func(c *Config) { c.TracesSampler = "huh" }, "TracesSampler"},
		{"ratio_missing_arg", func(c *Config) { c.TracesSampler = "traceidratio" }, "ratio"},
		{"ratio_out_of_range", func(c *Config) {
			c.TracesSampler = "parentbased_traceidratio"
			c.TracesSamplerArg = "1.5"
		}, "out of"},
		{"arg_set_for_non_ratio", func(c *Config) { c.TracesSamplerArg = "0.5" }, "must be empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validBaseline()
			tc.mut(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error = %q, want substring %q", err, tc.wantSub)
			}
		})
	}
}

func TestBuildSampler_AllNames(t *testing.T) {
	cases := []struct {
		name    string
		arg     string
		wantNil bool
	}{
		{"always_on", "", false},
		{"always_off", "", false},
		{"parentbased_always_on", "", false},
		{"parentbased_always_off", "", false},
		{"traceidratio", "0.5", false},
		{"parentbased_traceidratio", "0.1", false},
		{"unknown", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := buildSampler(tc.name, tc.arg)
			if tc.wantNil {
				if err == nil {
					t.Errorf("expected error for %s", tc.name)
				}
				return
			}
			if err != nil {
				t.Errorf("buildSampler(%s): %v", tc.name, err)
			}
			if s == nil {
				t.Errorf("sampler is nil for %s", tc.name)
			}
		})
	}
}

// restoreGlobals snapshots the global TracerProvider and TextMapPropagator
// before a Setup call and restores them on test cleanup. Setup mutates
// process-wide state, so without this every test would leak into the next.
func restoreGlobals(t *testing.T) {
	t.Helper()
	prevProp := otelapi.GetTextMapPropagator()
	prevTP := otelapi.GetTracerProvider()
	t.Cleanup(func() {
		otelapi.SetTextMapPropagator(prevProp)
		otelapi.SetTracerProvider(prevTP)
	})
}

// TestSetup_PropagatorRoundTrip starts a sampled span and verifies the
// configured propagator can inject and extract its SpanContext via a
// MapCarrier. Exercises the W3C TraceContext propagator wired by Setup.
func TestSetup_PropagatorRoundTrip(t *testing.T) {
	restoreGlobals(t)
	shutdown, err := Setup(t.Context(), validBaseline())
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	tracer := otelapi.Tracer("pgrelay/test")
	ctx, span := tracer.Start(t.Context(), "round-trip")
	defer span.End()

	originalSC := span.SpanContext()
	if !originalSC.IsValid() {
		t.Fatal("original span has invalid SpanContext (sampler should have emitted IDs)")
	}

	carrier := propagation.MapCarrier{}
	otelapi.GetTextMapPropagator().Inject(ctx, carrier)

	if carrier.Get("traceparent") == "" {
		t.Error("traceparent not injected")
	}

	extractedCtx := otelapi.GetTextMapPropagator().Extract(context.Background(), carrier)
	extractedSC := trace.SpanContextFromContext(extractedCtx)

	if extractedSC.TraceID() != originalSC.TraceID() {
		t.Errorf("TraceID mismatch: got %s, want %s", extractedSC.TraceID(), originalSC.TraceID())
	}
	if extractedSC.SpanID() != originalSC.SpanID() {
		t.Errorf("SpanID mismatch: got %s, want %s", extractedSC.SpanID(), originalSC.SpanID())
	}
}

// TestSetup_NoEndpointStillSetsPropagator: when OTLPEndpoint is empty the
// exporter is skipped, but the propagator must still be installed.
func TestSetup_NoEndpointStillSetsPropagator(t *testing.T) {
	restoreGlobals(t)
	shutdown, err := Setup(t.Context(), validBaseline())
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	carrier := propagation.MapCarrier{
		"traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
	}
	ctx := otelapi.GetTextMapPropagator().Extract(context.Background(), carrier)
	sc := trace.SpanContextFromContext(ctx)

	if !sc.IsValid() {
		t.Fatal("expected propagator to extract a valid SpanContext from carrier")
	}
}
