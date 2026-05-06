// Package sinkmock provides a test-only sinks.Sink implementation for
// the dispatcher tests in #6. It records every Message passed to Send,
// supports a queue of errors to return on subsequent calls, and exposes
// a callback hook for dynamic per-call behavior. Production code must
// not import this package.
//
// For tests that assert dispatcher metric labels (e.g. histogram
// `sink="http"`), construct with the canonical sink name from the sinks
// package: sinkmock.New(sinks.SinkHTTP).
//
// SetOnSend takes precedence over EnqueueErrors. A test that sets a
// callback and forgets to clear it can mask later error-queue setup;
// call Reset between sub-tests reusing the same mock.
package sinkmock

import (
	"context"
	"sync"

	"github.com/ronango/pgrelay/internal/outbox/sinks"
)

// Compile-time check that *Sink satisfies sinks.Sink.
var _ sinks.Sink = (*Sink)(nil)

// SendFunc is the signature of the per-call callback installed via
// SetOnSend. The ctx is the same one passed to Send so cancellation
// tests can observe shutdown propagation.
type SendFunc func(ctx context.Context, msg sinks.Message) error

// Sink is a thread-safe mock of sinks.Sink.
type Sink struct {
	mu     sync.Mutex
	name   string
	sent   []sinks.Message
	errs   []error
	onSend SendFunc
}

// New returns a Sink with the given name. Empty name defaults to "mock"
// so tests can construct a baseline mock without arguments.
func New(name string) *Sink {
	if name == "" {
		name = "mock"
	}
	return &Sink{name: name}
}

// Name implements sinks.Sink.
func (s *Sink) Name() string { return s.name }

// Send records msg and returns the next queued behavior. Precedence:
// onSend callback (if set) takes priority; otherwise the head of the
// errors queue is consumed; once exhausted, Send returns nil.
func (s *Sink) Send(ctx context.Context, msg sinks.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sent = append(s.sent, msg)

	if s.onSend != nil {
		return s.onSend(ctx, msg)
	}
	if len(s.errs) > 0 {
		err := s.errs[0]
		s.errs = s.errs[1:]
		return err
	}
	return nil
}

// Sent returns a snapshot of recorded messages in receive order.
func (s *Sink) Sent() []sinks.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]sinks.Message, len(s.sent))
	copy(out, s.sent)
	return out
}

// SentCount returns the number of recorded messages without allocating.
func (s *Sink) SentCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

// EnqueueErrors appends to the per-call error queue. nil entries are
// successes; the queue is consumed in order. After exhaustion Send
// returns nil. Multiple calls compose: queueing 3 errors then 2 more
// yields a 5-deep queue.
func (s *Sink) EnqueueErrors(errs ...error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs = append(s.errs, errs...)
}

// SetOnSend installs a callback invoked on every Send. Takes precedence
// over EnqueueErrors. Pass nil to clear.
func (s *Sink) SetOnSend(fn SendFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onSend = fn
}

// Reset clears recorded messages, the error queue, and the onSend hook.
// Useful between sub-tests reusing the same mock.
func (s *Sink) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = nil
	s.errs = nil
	s.onSend = nil
}
