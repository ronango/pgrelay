package sinks_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ronango/pgrelay/internal/outbox/sinks"
)

func TestHTTPSink_Name(t *testing.T) {
	if got := sinks.NewHTTPSink().Name(); got != sinks.SinkHTTP {
		t.Errorf("Name() = %q, want %q", got, sinks.SinkHTTP)
	}
}

func TestHTTPSink_PostsPayloadVerbatim(t *testing.T) {
	const payload = `{"event":"order.created","total":"42.00"}`

	var got struct {
		method      string
		path        string
		body        string
		contentType string
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.path = r.URL.Path
		got.contentType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		got.body = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	sink := sinks.NewHTTPSink(sinks.WithHTTPClient(srv.Client()))
	err := sink.Send(t.Context(), sinks.Message{
		ID:          42,
		Destination: srv.URL + "/events",
		Payload:     []byte(payload),
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got.method != http.MethodPost {
		t.Errorf("method = %s, want POST", got.method)
	}
	if got.path != "/events" {
		t.Errorf("path = %s, want /events", got.path)
	}
	if got.body != payload {
		t.Errorf("body = %q, want %q", got.body, payload)
	}
	if got.contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got.contentType)
	}
}

func TestHTTPSink_SetsReservedHeaders(t *testing.T) {
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	sink := sinks.NewHTTPSink(sinks.WithHTTPClient(srv.Client()))
	err := sink.Send(t.Context(), sinks.Message{
		ID:          12345,
		Destination: srv.URL,
		Payload:     []byte(`{}`),
		Traceparent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		Tracestate:  "rojo=00f067aa0ba902b7",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got := seen.Get("Idempotency-Key"); got != "12345" {
		t.Errorf("Idempotency-Key = %q, want 12345", got)
	}
	if got := seen.Get("Traceparent"); got != "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01" {
		t.Errorf("Traceparent = %q", got)
	}
	if got := seen.Get("Tracestate"); got != "rojo=00f067aa0ba902b7" {
		t.Errorf("Tracestate = %q", got)
	}
}

func TestHTTPSink_UserHeadersCannotOverwriteReserved(t *testing.T) {
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	sink := sinks.NewHTTPSink(sinks.WithHTTPClient(srv.Client()))
	err := sink.Send(t.Context(), sinks.Message{
		ID:          1,
		Destination: srv.URL,
		Payload:     []byte(`{}`),
		Traceparent: "tp-real",
		Tracestate:  "ts-real",
		Headers: map[string]string{
			"content-type":     "text/plain",           // canonical-form variant
			"IDEMPOTENCY-KEY":  "user-trying-to-spoof", // upper-case variant
			"traceparent":      "tp-spoof",
			"TRACESTATE":       "ts-spoof",
			"X-Custom-Header":  "kept",
			"X-Another-Header": "kept-too",
		},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Reserved still set by sink.
	if got := seen.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, user header leaked through", got)
	}
	if got := seen.Get("Idempotency-Key"); got != "1" {
		t.Errorf("Idempotency-Key = %q, user header leaked through", got)
	}
	if got := seen.Get("Traceparent"); got != "tp-real" {
		t.Errorf("Traceparent = %q, user header leaked through", got)
	}
	if got := seen.Get("Tracestate"); got != "ts-real" {
		t.Errorf("Tracestate = %q, user header leaked through", got)
	}
	// Custom user headers passed through unchanged.
	if got := seen.Get("X-Custom-Header"); got != "kept" {
		t.Errorf("X-Custom-Header = %q, want kept", got)
	}
	if got := seen.Get("X-Another-Header"); got != "kept-too" {
		t.Errorf("X-Another-Header = %q, want kept-too", got)
	}
}

func TestHTTPSink_SuccessStatusCodes(t *testing.T) {
	for _, code := range []int{200, 201, 202, 204} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			srv := newStatusServer(t, code, "", nil)
			sink := sinks.NewHTTPSink(sinks.WithHTTPClient(srv.Client()))
			if err := sink.Send(t.Context(), msgFor(srv.URL)); err != nil {
				t.Errorf("Send for %d returned %v, want nil", code, err)
			}
		})
	}
}

func TestHTTPSink_RetryableStatusCodes(t *testing.T) {
	for _, code := range []int{408, 425, 429, 500, 502, 503, 504} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			srv := newStatusServer(t, code, "", nil)
			sink := sinks.NewHTTPSink(sinks.WithHTTPClient(srv.Client()))

			err := sink.Send(t.Context(), msgFor(srv.URL))
			if !sinks.IsRetryable(err) {
				t.Errorf("Send for %d returned %v, want *RetryableError", code, err)
			}
		})
	}
}

func TestHTTPSink_TerminalStatusCodes(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 422, 451} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			srv := newStatusServer(t, code, "", nil)
			sink := sinks.NewHTTPSink(sinks.WithHTTPClient(srv.Client()))

			err := sink.Send(t.Context(), msgFor(srv.URL))
			if err == nil {
				t.Fatalf("Send for %d returned nil, want terminal error", code)
			}
			if sinks.IsRetryable(err) {
				t.Errorf("Send for %d classified as retryable, want terminal", code)
			}
		})
	}
}

func TestHTTPSink_3xxTerminal(t *testing.T) {
	// CheckRedirect: ErrUseLastResponse — 3xx must surface as terminal.
	// We rely on the sink's own client (which carries the CheckRedirect
	// policy); httptest.NewServer is plain HTTP so the default transport
	// reaches it without TLS gymnastics.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://elsewhere.example/")
		w.WriteHeader(http.StatusMovedPermanently)
	}))
	t.Cleanup(srv.Close)

	sink := sinks.NewHTTPSink()
	err := sink.Send(t.Context(), msgFor(srv.URL))
	if err == nil {
		t.Fatal("Send returned nil for 301, want terminal")
	}
	if sinks.IsRetryable(err) {
		t.Errorf("301 classified as retryable, want terminal: %v", err)
	}
}

func TestHTTPSink_RetryAfterDeltaSeconds(t *testing.T) {
	srv := newStatusServer(t, http.StatusTooManyRequests, "", http.Header{
		"Retry-After": []string{"7"},
	})
	sink := sinks.NewHTTPSink(sinks.WithHTTPClient(srv.Client()))

	err := sink.Send(t.Context(), msgFor(srv.URL))
	re, ok := sinks.AsRetryable(err)
	if !ok {
		t.Fatalf("Send returned %v, want *RetryableError", err)
	}
	if re.RetryAfter != 7*time.Second {
		t.Errorf("RetryAfter = %s, want 7s", re.RetryAfter)
	}
}

func TestHTTPSink_RetryAfterHTTPDate(t *testing.T) {
	future := time.Now().Add(15 * time.Second).UTC().Format(http.TimeFormat)
	srv := newStatusServer(t, http.StatusServiceUnavailable, "", http.Header{
		"Retry-After": []string{future},
	})
	sink := sinks.NewHTTPSink(sinks.WithHTTPClient(srv.Client()))

	err := sink.Send(t.Context(), msgFor(srv.URL))
	re, ok := sinks.AsRetryable(err)
	if !ok {
		t.Fatalf("Send returned %v, want *RetryableError", err)
	}
	// Tolerance: HTTP-date has 1-second resolution and parsing takes
	// real time on slow CI; assert "tracks the encoded value" rather
	// than a tight bound.
	if re.RetryAfter < 10*time.Second || re.RetryAfter > 20*time.Second {
		t.Errorf("RetryAfter = %s, want roughly 15s", re.RetryAfter)
	}
}

func TestHTTPSink_RetryAfterAbsentOrUnparseable(t *testing.T) {
	cases := []struct {
		name string
		set  bool // false = header omitted; true = set to value (incl. "")
		val  string
	}{
		{name: "absent", set: false},
		{name: "empty_string", set: true, val: ""},
		{name: "garbage", set: true, val: "soon"},
		{name: "past_date", set: true, val: time.Now().Add(-1 * time.Hour).UTC().Format(http.TimeFormat)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			if tc.set {
				h["Retry-After"] = []string{tc.val}
			}
			srv := newStatusServer(t, http.StatusServiceUnavailable, "", h)
			sink := sinks.NewHTTPSink(sinks.WithHTTPClient(srv.Client()))

			re, ok := sinks.AsRetryable(sink.Send(t.Context(), msgFor(srv.URL)))
			if !ok {
				t.Fatal("expected *RetryableError")
			}
			if re.RetryAfter != 0 {
				t.Errorf("RetryAfter = %s, want 0", re.RetryAfter)
			}
		})
	}
}

func TestHTTPSink_NetworkErrorRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.Close() // close before sending so connect refuses

	sink := sinks.NewHTTPSink(sinks.WithHTTPClient(srv.Client()))
	err := sink.Send(t.Context(), msgFor(srv.URL))
	if !sinks.IsRetryable(err) {
		t.Errorf("network error not classified as retryable: %v", err)
	}
}

func TestHTTPSink_ContextCanceledIsTerminal(t *testing.T) {
	// Pre-canceled ctx — http.NewRequestWithContext succeeds (it doesn't
	// inspect ctx state); client.Do is the call that surfaces the ctx
	// error, which exercises the `errors.Is(err, context.Canceled)`
	// branch in Send and returns ctx.Err() instead of wrapping as
	// retryable. That branch is the whole point of this test.
	srv := newStatusServer(t, http.StatusOK, "", nil)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	sink := sinks.NewHTTPSink(sinks.WithHTTPClient(srv.Client()))
	err := sink.Send(ctx, msgFor(srv.URL))

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Send returned %v, want context.Canceled", err)
	}
	// The whole point of the special-case: dispatcher shutdown isn't transient.
	if sinks.IsRetryable(err) {
		t.Error("context.Canceled was classified as retryable")
	}
}

func TestHTTPSink_EmptyDestinationTerminal(t *testing.T) {
	sink := sinks.NewHTTPSink()
	err := sink.Send(t.Context(), sinks.Message{ID: 1, Payload: []byte(`{}`)})
	if err == nil {
		t.Fatal("Send returned nil for empty destination")
	}
	if sinks.IsRetryable(err) {
		t.Error("empty destination classified as retryable")
	}
	if !strings.Contains(err.Error(), "destination") {
		t.Errorf("error %q should mention 'destination' for diagnosability", err)
	}
}

func TestHTTPSink_RejectsMalformedHeaderFields(t *testing.T) {
	// Producer can write control bytes (CR/LF/NUL, DEL, other <0x20)
	// into traceparent/tracestate/headers. Without validation the row
	// is retried up to MaxAttempts; the sink rejects terminally instead.
	cases := []struct {
		name string
		msg  sinks.Message
		want string
	}{
		{
			name: "traceparent_with_lf",
			msg:  sinks.Message{ID: 1, Destination: "http://x.test", Payload: []byte("{}"), Traceparent: "tp\nX-Admin: 1"},
			want: "traceparent",
		},
		{
			name: "tracestate_with_cr",
			msg:  sinks.Message{ID: 1, Destination: "http://x.test", Payload: []byte("{}"), Tracestate: "ts\rinjected"},
			want: "tracestate",
		},
		{
			name: "user_header_value_with_nul",
			msg: sinks.Message{
				ID: 1, Destination: "http://x.test", Payload: []byte("{}"),
				Headers: map[string]string{"X-Custom": "value\x00null"},
			},
			want: "X-Custom",
		},
		{
			name: "user_header_name_with_space",
			msg: sinks.Message{
				ID: 1, Destination: "http://x.test", Payload: []byte("{}"),
				Headers: map[string]string{"X Bad": "ok"},
			},
			want: "X Bad",
		},
		{
			name: "user_header_name_empty",
			msg: sinks.Message{
				ID: 1, Destination: "http://x.test", Payload: []byte("{}"),
				Headers: map[string]string{"": "ok"},
			},
			want: "RFC 7230",
		},
		{
			name: "traceparent_with_nul",
			msg:  sinks.Message{ID: 1, Destination: "http://x.test", Payload: []byte("{}"), Traceparent: "tp\x00inj"},
			want: "traceparent",
		},
		{
			name: "header_value_with_del",
			msg: sinks.Message{
				ID: 1, Destination: "http://x.test", Payload: []byte("{}"),
				Headers: map[string]string{"X-Custom": "before\x7fafter"},
			},
			want: "X-Custom",
		},
	}
	sink := sinks.NewHTTPSink()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := sink.Send(t.Context(), tc.msg)
			if err == nil {
				t.Fatal("Send returned nil for malformed header")
			}
			if sinks.IsRetryable(err) {
				t.Errorf("malformed header classified as retryable: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q missing %q for diagnosability", err, tc.want)
			}
		})
	}
}

func TestHTTPSink_ErrorIncludesBodySnippet(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"terminal_4xx", http.StatusForbidden, `{"error":"account suspended"}`, "account suspended"},
		{"retryable_5xx", http.StatusServiceUnavailable, `{"error":"upstream queue full"}`, "upstream queue full"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newStatusServer(t, tc.status, tc.body, nil)
			sink := sinks.NewHTTPSink(sinks.WithHTTPClient(srv.Client()))

			err := sink.Send(t.Context(), msgFor(srv.URL))
			if err == nil {
				t.Fatal("Send returned nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q missing body snippet %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), strconv.Itoa(tc.status)) {
				t.Errorf("error %q missing status code %d", err, tc.status)
			}
		})
	}
}

// --- helpers ---

func msgFor(url string) sinks.Message {
	return sinks.Message{
		ID:          1,
		Destination: url,
		Payload:     []byte(`{}`),
	}
}

// newStatusServer responds with the given status, optional body, and
// optional header set. Cleanup is registered on t.
func newStatusServer(t *testing.T, status int, body string, headers http.Header) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for k, vs := range headers {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(status)
		if body != "" {
			_, _ = fmt.Fprint(w, body)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}
