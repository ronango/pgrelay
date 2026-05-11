package outbox

import (
	"math/rand/v2"
	"sync"
	"time"

	"github.com/ronango/pgrelay/internal/config"
)

// Policy carries retry parameters plus a private RNG guarded by a
// mutex. math/rand/v2's package-level helpers are race-safe but
// share a single global Source, which makes test determinism
// impossible — hence the seamed RNG.
type Policy struct {
	Base        time.Duration
	Max         time.Duration
	Jitter      float64
	MaxAttempts int

	mu  sync.Mutex
	rng *rand.Rand
}

// NewPolicy builds a Policy with a time-seeded RNG.
func NewPolicy(cfg config.Config) *Policy {
	n := uint64(time.Now().UnixNano()) //nolint:gosec // non-crypto jitter
	return NewPolicyWithRand(cfg, rand.New(rand.NewPCG(n, n^0x9E3779B97F4A7C15)))
}

// NewPolicyWithRand seams the RNG so tests can assert exact delays.
func NewPolicyWithRand(cfg config.Config, rng *rand.Rand) *Policy {
	return &Policy{
		Base:        cfg.RetryBase,
		Max:         cfg.RetryMax,
		Jitter:      cfg.RetryJitter,
		MaxAttempts: cfg.MaxAttempts,
		rng:         rng,
	}
}

// NextDelay returns the wait before the next attempt. attempt is the
// row's `attempts` count after Claim's increment (so 1 on the first
// retry). Safe for concurrent use.
func (p *Policy) NextDelay(attempt int) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Backoff(attempt, p.Base, p.Max, p.Jitter, p.rng)
}
