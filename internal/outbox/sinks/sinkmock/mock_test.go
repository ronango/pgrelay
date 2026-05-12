package sinkmock_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/ronango/pgrelay/internal/outbox/sinks"
	"github.com/ronango/pgrelay/internal/outbox/sinks/sinkmock"
)

func TestNew_DefaultName(t *testing.T) {
	if got := sinkmock.New("").Name(); got != "mock" {
		t.Errorf("Name() = %q, want mock", got)
	}
}

func TestNew_CustomName(t *testing.T) {
	if got := sinkmock.New(sinks.SinkHTTP).Name(); got != "http" {
		t.Errorf("Name() = %q, want http", got)
	}
}

func TestSink_RecordsSentMessages(t *testing.T) {
	m := sinkmock.New("")

	for _, id := range []int64{1, 2, 3} {
		if err := m.Send(t.Context(), sinks.Message{ID: id}); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}

	if got := m.SentCount(); got != 3 {
		t.Errorf("SentCount = %d, want 3", got)
	}
	got := m.Sent()
	if len(got) != 3 {
		t.Fatalf("Sent len = %d, want 3", len(got))
	}
	for i, id := range []int64{1, 2, 3} {
		if got[i].ID != id {
			t.Errorf("Sent[%d].ID = %d, want %d", i, got[i].ID, id)
		}
	}
}

func TestSink_EnqueueErrorsConsumesInOrder(t *testing.T) {
	boom := errors.New("boom")
	m := sinkmock.New("")
	m.EnqueueErrors(boom, nil, boom)

	results := make([]error, 4)
	for i := range results {
		results[i] = m.Send(t.Context(), sinks.Message{ID: int64(i)})
	}

	wants := []error{boom, nil, boom, nil}
	for i, want := range wants {
		if !errors.Is(results[i], want) {
			t.Errorf("Send[%d] = %v, want %v", i, results[i], want)
		}
	}
}

func TestSink_EnqueueErrorsAppends(t *testing.T) {
	a, b := errors.New("a"), errors.New("b")
	m := sinkmock.New("")
	m.EnqueueErrors(a)
	m.EnqueueErrors(b)

	results := []error{
		m.Send(t.Context(), sinks.Message{ID: 1}),
		m.Send(t.Context(), sinks.Message{ID: 2}),
	}
	if !errors.Is(results[0], a) || !errors.Is(results[1], b) {
		t.Errorf("got %v, want [a, b]", results)
	}
}

func TestSink_OnSendCallbackTakesPrecedence(t *testing.T) {
	m := sinkmock.New("")
	m.EnqueueErrors(errors.New("queued"))

	called := 0
	m.SetOnSend(func(_ context.Context, msg sinks.Message) error {
		called++
		if msg.ID%2 == 0 {
			return errors.New("even ids fail")
		}
		return nil
	})

	if err := m.Send(t.Context(), sinks.Message{ID: 1}); err != nil {
		t.Errorf("ID=1: %v, want nil", err)
	}
	if err := m.Send(t.Context(), sinks.Message{ID: 2}); err == nil || err.Error() != "even ids fail" {
		t.Errorf("ID=2: %v, want even-ids-fail", err)
	}
	if called != 2 {
		t.Errorf("callback called %d times, want 2", called)
	}
}

func TestSink_OnSendReceivesContext(t *testing.T) {
	m := sinkmock.New("")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var got error
	m.SetOnSend(func(ctx context.Context, _ sinks.Message) error {
		got = ctx.Err()
		return got
	})

	if err := m.Send(ctx, sinks.Message{ID: 1}); !errors.Is(err, context.Canceled) {
		t.Errorf("Send returned %v, want context.Canceled (callback should observe)", err)
	}
	if !errors.Is(got, context.Canceled) {
		t.Errorf("callback observed ctx.Err = %v, want context.Canceled", got)
	}
}

func TestSink_Reset_ClearsAllState(t *testing.T) {
	m := sinkmock.New("")
	_ = m.Send(t.Context(), sinks.Message{ID: 1})
	m.EnqueueErrors(errors.New("x"))
	m.SetOnSend(func(context.Context, sinks.Message) error { return nil })

	m.Reset()

	if got := m.SentCount(); got != 0 {
		t.Errorf("SentCount after Reset = %d, want 0", got)
	}
	if err := m.Send(t.Context(), sinks.Message{ID: 99}); err != nil {
		t.Errorf("Send after Reset: %v, want nil (queue/callback both cleared)", err)
	}
}

func TestSink_ConcurrentSendsAreSafe(t *testing.T) {
	m := sinkmock.New("")
	const goroutines = 50
	const perGoroutine = 20

	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for i := range perGoroutine {
				_ = m.Send(context.Background(), sinks.Message{ID: int64(base*perGoroutine + i)})
			}
		}(g)
	}
	wg.Wait()

	const total = goroutines * perGoroutine
	sent := m.Sent()
	if len(sent) != total {
		t.Fatalf("Sent len = %d, want %d", len(sent), total)
	}

	got := make([]int64, len(sent))
	for i, msg := range sent {
		got[i] = msg.ID
	}
	slices.Sort(got)
	for i := 0; i < total; i++ {
		if got[i] != int64(i) {
			t.Fatalf("missing or duplicated ID under contention: got[%d] = %d", i, got[i])
		}
	}
}
