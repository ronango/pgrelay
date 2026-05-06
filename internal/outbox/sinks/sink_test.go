package sinks_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ronango/pgrelay/internal/outbox/sinks"
)

func TestRetryableError_Error(t *testing.T) {
	cases := []struct {
		name string
		err  *sinks.RetryableError
		want string
	}{
		{
			name: "with_retry_after",
			err:  &sinks.RetryableError{Cause: errors.New("503 Service Unavailable"), RetryAfter: 5 * time.Second},
			want: "retryable (after 5s): 503 Service Unavailable",
		},
		{
			name: "no_retry_after",
			err:  &sinks.RetryableError{Cause: errors.New("connection reset")},
			want: "retryable: connection reset",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRetryableError_Unwrap(t *testing.T) {
	cause := errors.New("boom")
	wrapped := &sinks.RetryableError{Cause: cause}

	if got := errors.Unwrap(wrapped); !errors.Is(got, cause) {
		t.Errorf("Unwrap = %v, want %v", got, cause)
	}
	if !errors.Is(wrapped, cause) {
		t.Error("errors.Is should match wrapped cause")
	}
}

func TestIsRetryable(t *testing.T) {
	retryable := &sinks.RetryableError{Cause: errors.New("503")}
	nilCause := &sinks.RetryableError{} // zero-value is still retryable

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain_error", errors.New("terminal"), false},
		{"direct_retryable", retryable, true},
		{"wrapped_once", fmt.Errorf("send: %w", retryable), true},
		{"wrapped_twice", fmt.Errorf("dispatch: %w", fmt.Errorf("send: %w", retryable)), true},
		{"nil_cause_still_retryable", nilCause, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sinks.IsRetryable(tc.err); got != tc.want {
				t.Errorf("IsRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestAsRetryable(t *testing.T) {
	retryable := sinks.NewRetryableError(errors.New("503"), 5*time.Second)

	cases := []struct {
		name        string
		err         error
		wantOK      bool
		wantRA      time.Duration
		wantCauseOK bool
	}{
		{"plain_error", errors.New("x"), false, 0, false},
		{"retryable_with_after", retryable, true, 5 * time.Second, true},
		{"retryable_wrapped", fmt.Errorf("send: %w", retryable), true, 5 * time.Second, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			re, ok := sinks.AsRetryable(tc.err)
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if re.RetryAfter != tc.wantRA {
				t.Errorf("RetryAfter = %s, want %s", re.RetryAfter, tc.wantRA)
			}
		})
	}
}

func TestNewRetryableError_ClampsNegativeRetryAfter(t *testing.T) {
	got := sinks.NewRetryableError(errors.New("x"), -5*time.Second)
	if got.RetryAfter != 0 {
		t.Errorf("RetryAfter = %s, want 0 (negative clamped)", got.RetryAfter)
	}
}
