package config

import (
	"strings"
	"testing"
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
