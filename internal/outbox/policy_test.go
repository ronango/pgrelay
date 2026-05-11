package outbox_test

import (
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/ronango/pgrelay/internal/config"
	"github.com/ronango/pgrelay/internal/outbox"
)

func testCfg() config.Config {
	return config.Config{
		RetryBase:   time.Second,
		RetryMax:    time.Minute,
		RetryJitter: 0.2,
		MaxAttempts: 10,
	}
}

func TestPolicy_NextDelayMatchesBackoffWithSameRNG(t *testing.T) {
	cfg := testCfg()

	// Two RNGs from identical seeds → Policy.NextDelay must produce
	// the same sequence as a direct Backoff call.
	rngPolicy := rand.New(rand.NewPCG(42, 0))
	rngDirect := rand.New(rand.NewPCG(42, 0))
	p := outbox.NewPolicyWithRand(cfg, rngPolicy)

	for attempt := 1; attempt <= 5; attempt++ {
		gotPolicy := p.NextDelay(attempt)
		gotDirect := outbox.Backoff(attempt, cfg.RetryBase, cfg.RetryMax, cfg.RetryJitter, rngDirect)
		if gotPolicy != gotDirect {
			t.Errorf("attempt=%d: Policy.NextDelay=%s, direct Backoff=%s", attempt, gotPolicy, gotDirect)
		}
	}
}

func TestPolicy_NextDelayDeterministicAcrossSeededInstances(t *testing.T) {
	cfg := testCfg()
	p1 := outbox.NewPolicyWithRand(cfg, rand.New(rand.NewPCG(1, 2)))
	p2 := outbox.NewPolicyWithRand(cfg, rand.New(rand.NewPCG(1, 2)))

	for attempt := 1; attempt <= 8; attempt++ {
		d1 := p1.NextDelay(attempt)
		d2 := p2.NextDelay(attempt)
		if d1 != d2 {
			t.Errorf("attempt=%d: instances with same seed diverged: %s vs %s", attempt, d1, d2)
		}
	}
}

func TestPolicy_NextDelayConcurrentSafe(t *testing.T) {
	// Run under `go test -race`; the RNG must be guarded.
	cfg := testCfg()
	p := outbox.NewPolicyWithRand(cfg, rand.New(rand.NewPCG(7, 11)))

	const goroutines = 32
	const callsEach = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for i := 1; i <= callsEach; i++ {
				_ = p.NextDelay(i)
			}
		}()
	}
	wg.Wait()
}

func TestNewPolicy_NonDeterministicAcrossCalls(t *testing.T) {
	// Catches the obvious regression of seeding from a constant. Two
	// NewPolicy calls 1µs apart must produce divergent streams.
	cfg := testCfg()
	p1 := outbox.NewPolicy(cfg)
	time.Sleep(time.Microsecond)
	p2 := outbox.NewPolicy(cfg)

	// Compare 5 attempts; collision across all 5 with independent seeds
	// is astronomically unlikely.
	allSame := true
	for attempt := 1; attempt <= 5; attempt++ {
		if p1.NextDelay(attempt) != p2.NextDelay(attempt) {
			allSame = false
			break
		}
	}
	if allSame {
		t.Error("two NewPolicy calls produced identical delays — RNG seeding likely broken")
	}
}

func TestPolicy_FieldsMirrorConfig(t *testing.T) {
	cfg := testCfg()
	p := outbox.NewPolicy(cfg)

	if p.Base != cfg.RetryBase {
		t.Errorf("Base = %s, want %s", p.Base, cfg.RetryBase)
	}
	if p.Max != cfg.RetryMax {
		t.Errorf("Max = %s, want %s", p.Max, cfg.RetryMax)
	}
	if p.Jitter != cfg.RetryJitter {
		t.Errorf("Jitter = %v, want %v", p.Jitter, cfg.RetryJitter)
	}
	if p.MaxAttempts != cfg.MaxAttempts {
		t.Errorf("MaxAttempts = %d, want %d", p.MaxAttempts, cfg.MaxAttempts)
	}
}
