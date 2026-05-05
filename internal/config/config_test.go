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
