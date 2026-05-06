package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoad_Success(t *testing.T) {
	t.Setenv("PGRELAY_DATABASE_URL", "postgres://localhost/test")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DatabaseURL != "postgres://localhost/test" {
		t.Errorf("DatabaseURL = %q, want postgres://localhost/test", cfg.DatabaseURL)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want default 'info'", cfg.LogLevel)
	}
}

func TestLoad_PoolDefaults(t *testing.T) {
	t.Setenv("PGRELAY_DATABASE_URL", "postgres://localhost/test")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"DBMinConns", cfg.DBMinConns, int32(1)},
		{"DBMaxConns", cfg.DBMaxConns, int32(10)},
		{"DBMaxConnLifetime", cfg.DBMaxConnLifetime, time.Hour},
		{"DBMaxConnIdleTime", cfg.DBMaxConnIdleTime, 30 * time.Minute},
		{"DBHealthCheckPeriod", cfg.DBHealthCheckPeriod, time.Minute},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestLoad_PoolOverrides(t *testing.T) {
	t.Setenv("PGRELAY_DATABASE_URL", "postgres://localhost/test")
	t.Setenv("PGRELAY_DB_MIN_CONNS", "5")
	t.Setenv("PGRELAY_DB_MAX_CONNS", "25")
	t.Setenv("PGRELAY_DB_MAX_CONN_LIFETIME", "2h")
	t.Setenv("PGRELAY_DB_MAX_CONN_IDLE_TIME", "15m")
	t.Setenv("PGRELAY_DB_HEALTH_CHECK_PERIOD", "30s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"DBMinConns", cfg.DBMinConns, int32(5)},
		{"DBMaxConns", cfg.DBMaxConns, int32(25)},
		{"DBMaxConnLifetime", cfg.DBMaxConnLifetime, 2 * time.Hour},
		{"DBMaxConnIdleTime", cfg.DBMaxConnIdleTime, 15 * time.Minute},
		{"DBHealthCheckPeriod", cfg.DBHealthCheckPeriod, 30 * time.Second},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestLoad_DatabaseURLEmpty(t *testing.T) {
	t.Setenv("PGRELAY_DATABASE_URL", "")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for empty PGRELAY_DATABASE_URL, got nil")
	}
}

func TestLoad_DatabaseURLBadScheme(t *testing.T) {
	t.Setenv("PGRELAY_DATABASE_URL", "mysql://localhost/test")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for non-postgres scheme, got nil")
	}
	if !strings.Contains(err.Error(), "PGRELAY_DATABASE_URL") {
		t.Errorf("error = %q, want it to mention PGRELAY_DATABASE_URL", err)
	}
}

func TestLoad_LogLevelOverride(t *testing.T) {
	t.Setenv("PGRELAY_DATABASE_URL", "postgres://localhost/test")
	t.Setenv("PGRELAY_LOG_LEVEL", "debug")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
}

func TestLoad_LogLevelInvalid(t *testing.T) {
	t.Setenv("PGRELAY_DATABASE_URL", "postgres://localhost/test")
	t.Setenv("PGRELAY_LOG_LEVEL", "infor")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid log level, got nil")
	}
	if !strings.Contains(err.Error(), "PGRELAY_LOG_LEVEL") {
		t.Errorf("error = %q, want it to mention PGRELAY_LOG_LEVEL", err)
	}
}

func TestLoad_PoolMalformed(t *testing.T) {
	// env library handles parse failures; we just verify they propagate.
	cases := []struct {
		name     string
		envKey   string
		envValue string
	}{
		{"malformed_int", "PGRELAY_DB_MAX_CONNS", "abc"},
		{"malformed_duration", "PGRELAY_DB_MAX_CONN_LIFETIME", "oops"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PGRELAY_DATABASE_URL", "postgres://localhost/test")
			t.Setenv(tc.envKey, tc.envValue)

			if _, err := Load(); err == nil {
				t.Fatalf("expected parse error for %s=%q, got nil", tc.envKey, tc.envValue)
			}
		})
	}
}

func TestLoad_PoolOutOfRange(t *testing.T) {
	// Validate() emits errors that name the env var so operators can find the
	// faulty setting; assert the env var appears in the message.
	cases := []struct {
		name     string
		envKey   string
		envValue string
	}{
		{"negative_min_conns", "PGRELAY_DB_MIN_CONNS", "-1"},
		{"zero_max_conns", "PGRELAY_DB_MAX_CONNS", "0"},
		{"negative_max_conn_lifetime", "PGRELAY_DB_MAX_CONN_LIFETIME", "-1h"},
		{"negative_max_conn_idle_time", "PGRELAY_DB_MAX_CONN_IDLE_TIME", "-1m"},
		{"negative_health_check_period", "PGRELAY_DB_HEALTH_CHECK_PERIOD", "-30s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PGRELAY_DATABASE_URL", "postgres://localhost/test")
			t.Setenv(tc.envKey, tc.envValue)

			_, err := Load()
			if err == nil {
				t.Fatalf("expected validation error for %s=%q, got nil", tc.envKey, tc.envValue)
			}
			if !strings.Contains(err.Error(), tc.envKey) {
				t.Errorf("error = %q, want it to mention %s", err, tc.envKey)
			}
		})
	}
}

func TestLoad_PoolMinExceedsMax(t *testing.T) {
	t.Setenv("PGRELAY_DATABASE_URL", "postgres://localhost/test")
	t.Setenv("PGRELAY_DB_MIN_CONNS", "20")
	t.Setenv("PGRELAY_DB_MAX_CONNS", "10")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for MIN > MAX, got nil")
	}
	for _, sub := range []string{"PGRELAY_DB_MIN_CONNS", "PGRELAY_DB_MAX_CONNS"} {
		if !strings.Contains(err.Error(), sub) {
			t.Errorf("error = %q, want it to mention %s", err, sub)
		}
	}
}

func TestLoad_ObservabilityDefaults(t *testing.T) {
	t.Setenv("PGRELAY_DATABASE_URL", "postgres://localhost/test")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.OpsAddr != ":9090" {
		t.Errorf("OpsAddr = %q, want :9090", cfg.OpsAddr)
	}
	if cfg.OTLPEndpoint != "" {
		t.Errorf("OTLPEndpoint = %q, want empty (OTel disabled by default)", cfg.OTLPEndpoint)
	}
	if len(cfg.OTLPHeaders) != 0 {
		t.Errorf("OTLPHeaders = %v, want empty map", cfg.OTLPHeaders)
	}
	if cfg.TracesSampler != "parentbased_always_on" {
		t.Errorf("TracesSampler = %q, want parentbased_always_on", cfg.TracesSampler)
	}
	if cfg.TracesSamplerArg != "" {
		t.Errorf("TracesSamplerArg = %q, want empty", cfg.TracesSamplerArg)
	}
}

func TestLoad_ObservabilityOverrides(t *testing.T) {
	t.Setenv("PGRELAY_DATABASE_URL", "postgres://localhost/test")
	t.Setenv("PGRELAY_OPS_ADDR", "127.0.0.1:8081")
	t.Setenv("PGRELAY_OTEL_OTLP_ENDPOINT", "http://collector:4318")
	t.Setenv("PGRELAY_OTEL_OTLP_HEADERS", "authorization=Bearer xyz,x-tenant=foo")
	t.Setenv("PGRELAY_OTEL_TRACES_SAMPLER", "parentbased_traceidratio")
	t.Setenv("PGRELAY_OTEL_TRACES_SAMPLER_ARG", "0.1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OpsAddr != "127.0.0.1:8081" {
		t.Errorf("OpsAddr = %q", cfg.OpsAddr)
	}
	if cfg.OTLPEndpoint != "http://collector:4318" {
		t.Errorf("OTLPEndpoint = %q", cfg.OTLPEndpoint)
	}
	if cfg.OTLPHeaders["authorization"] != "Bearer xyz" || cfg.OTLPHeaders["x-tenant"] != "foo" {
		t.Errorf("OTLPHeaders = %v, want {authorization: Bearer xyz, x-tenant: foo}", cfg.OTLPHeaders)
	}
	if cfg.TracesSampler != "parentbased_traceidratio" {
		t.Errorf("TracesSampler = %q", cfg.TracesSampler)
	}
	if cfg.TracesSamplerArg != "0.1" {
		t.Errorf("TracesSamplerArg = %q", cfg.TracesSamplerArg)
	}
}

func TestLoad_OpsAddrIPv6(t *testing.T) {
	t.Setenv("PGRELAY_DATABASE_URL", "postgres://localhost/test")
	t.Setenv("PGRELAY_OPS_ADDR", "[::1]:9090")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OpsAddr != "[::1]:9090" {
		t.Errorf("OpsAddr = %q, want [::1]:9090", cfg.OpsAddr)
	}
}

func TestLoad_ObservabilityInvalid(t *testing.T) {
	cases := []struct {
		name   string
		setEnv map[string]string
		envKey string // env var name expected in error message
	}{
		{
			name:   "ops_addr_missing_colon",
			setEnv: map[string]string{"PGRELAY_OPS_ADDR": "9090"},
			envKey: "PGRELAY_OPS_ADDR",
		},
		{
			name:   "otlp_endpoint_bad_scheme",
			setEnv: map[string]string{"PGRELAY_OTEL_OTLP_ENDPOINT": "ftp://collector:4318"},
			envKey: "PGRELAY_OTEL_OTLP_ENDPOINT",
		},
		{
			name:   "otlp_endpoint_missing_host",
			setEnv: map[string]string{"PGRELAY_OTEL_OTLP_ENDPOINT": "http://"},
			envKey: "PGRELAY_OTEL_OTLP_ENDPOINT",
		},
		{
			name: "sampler_arg_set_for_non_ratio",
			setEnv: map[string]string{
				"PGRELAY_OTEL_TRACES_SAMPLER":     "always_on",
				"PGRELAY_OTEL_TRACES_SAMPLER_ARG": "0.5",
			},
			envKey: "PGRELAY_OTEL_TRACES_SAMPLER_ARG",
		},
		{
			name:   "sampler_unknown",
			setEnv: map[string]string{"PGRELAY_OTEL_TRACES_SAMPLER": "random_sampler"},
			envKey: "PGRELAY_OTEL_TRACES_SAMPLER",
		},
		{
			name: "ratio_sampler_missing_arg",
			setEnv: map[string]string{
				"PGRELAY_OTEL_TRACES_SAMPLER": "traceidratio",
			},
			envKey: "PGRELAY_OTEL_TRACES_SAMPLER_ARG",
		},
		{
			name: "ratio_sampler_bad_arg",
			setEnv: map[string]string{
				"PGRELAY_OTEL_TRACES_SAMPLER":     "parentbased_traceidratio",
				"PGRELAY_OTEL_TRACES_SAMPLER_ARG": "abc",
			},
			envKey: "PGRELAY_OTEL_TRACES_SAMPLER_ARG",
		},
		{
			name: "ratio_sampler_out_of_range",
			setEnv: map[string]string{
				"PGRELAY_OTEL_TRACES_SAMPLER":     "traceidratio",
				"PGRELAY_OTEL_TRACES_SAMPLER_ARG": "1.5",
			},
			envKey: "PGRELAY_OTEL_TRACES_SAMPLER_ARG",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PGRELAY_DATABASE_URL", "postgres://localhost/test")
			for k, v := range tc.setEnv {
				t.Setenv(k, v)
			}

			_, err := Load()
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.envKey) {
				t.Errorf("error = %q, want substring %q", err, tc.envKey)
			}
		})
	}
}
