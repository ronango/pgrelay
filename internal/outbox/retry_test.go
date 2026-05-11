package outbox_test

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/ronango/pgrelay/internal/outbox"
)

// newRNG returns a deterministic *rand.Rand. Fixed seeds so jitter
// tests are reproducible across runs and across CPUs.
func newRNG() *rand.Rand {
	return rand.New(rand.NewPCG(0x9E3779B97F4A7C15, 0xBB67AE8584CAA73B))
}

func TestBackoff_ExponentialNoJitter(t *testing.T) {
	base := time.Second
	maxDelay := time.Hour // big enough that no clamp fires for these attempts

	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, time.Second},        // 2^0 * 1s
		{2, 2 * time.Second},    // 2^1 * 1s
		{3, 4 * time.Second},    // 2^2 * 1s
		{4, 8 * time.Second},    // 2^3 * 1s
		{10, 512 * time.Second}, // 2^9 * 1s = 8m32s
	}
	for _, tc := range cases {
		got := outbox.Backoff(tc.attempt, base, maxDelay, 0, nil)
		if got != tc.want {
			t.Errorf("Backoff(attempt=%d) = %s, want %s", tc.attempt, got, tc.want)
		}
	}
}

func TestBackoff_ClampsToMax(t *testing.T) {
	base := time.Second
	maxDelay := 5 * time.Minute

	// 2^9 = 512s = 8m32s > 5m → clamped.
	if got := outbox.Backoff(10, base, maxDelay, 0, nil); got != maxDelay {
		t.Errorf("Backoff(10, 1s, 5m, 0) = %s, want %s (clamped)", got, maxDelay)
	}
	// attempt=100 — huge but finite float64; clamped via the `<` check.
	if got := outbox.Backoff(100, base, maxDelay, 0, nil); got != maxDelay {
		t.Errorf("Backoff(100, ...) = %s, want %s (huge but finite, clamped)", got, maxDelay)
	}
	// attempt=2000 — math.Pow(2, 1999) overflows to +Inf; still clamped
	// because Inf is not less than any finite maxDelay.
	if got := outbox.Backoff(2000, base, maxDelay, 0, nil); got != maxDelay {
		t.Errorf("Backoff(2000, ...) = %s, want %s (+Inf clamped)", got, maxDelay)
	}
}

func TestBackoff_NonPositiveBaseReturnsZero(t *testing.T) {
	for _, base := range []time.Duration{0, -time.Second, -time.Hour} {
		if got := outbox.Backoff(3, base, time.Minute, 0, nil); got != 0 {
			t.Errorf("Backoff(3, %s, ...) = %s, want 0 (no-backoff semantics)", base, got)
		}
	}
}

func TestBackoff_NonPositiveMaxReturnsZero(t *testing.T) {
	for _, maxDelay := range []time.Duration{0, -time.Second} {
		if got := outbox.Backoff(3, time.Second, maxDelay, 0, nil); got != 0 {
			t.Errorf("Backoff(3, 1s, %s, 0) = %s, want 0", maxDelay, got)
		}
	}
}

func TestBackoff_JitterCannotExceedMax(t *testing.T) {
	const trials = 500
	// attempt=20 → 2^19 * 1s ≫ maxDelay, so pre-jitter delay == maxDelay.
	// With factor up to 1+jitter the unclamped product would exceed maxDelay;
	// the re-clamp must cap it.
	base := time.Second
	maxDelay := 10 * time.Second
	jitter := 0.5
	rng := newRNG()

	for range trials {
		got := outbox.Backoff(20, base, maxDelay, jitter, rng)
		if got > maxDelay {
			t.Fatalf("Backoff exceeded maxDelay: got %s, max %s", got, maxDelay)
		}
		if got < 0 {
			t.Fatalf("Backoff went negative: %s", got)
		}
	}
}

func TestBackoff_TreatsZeroOrNegativeAttemptAsFirst(t *testing.T) {
	base := 500 * time.Millisecond
	maxDelay := time.Minute

	for _, attempt := range []int{0, -1, -100} {
		got := outbox.Backoff(attempt, base, maxDelay, 0, nil)
		if got != base {
			t.Errorf("Backoff(%d) = %s, want %s (clamped to attempt=1)", attempt, got, base)
		}
	}
}

func TestBackoff_JitterStaysWithinBand(t *testing.T) {
	const trials = 200
	base := time.Second
	maxDelay := time.Minute
	jitter := 0.2
	rng := newRNG()

	// attempt=2 → 2s. With ±20% jitter: [1.6s, 2.4s].
	const wantCenter = 2 * time.Second
	low := time.Duration(float64(wantCenter) * (1 - jitter))
	high := time.Duration(float64(wantCenter) * (1 + jitter))

	var sum time.Duration
	for range trials {
		got := outbox.Backoff(2, base, maxDelay, jitter, rng)
		if got < low || got > high {
			t.Errorf("Backoff with jitter = %s, want in [%s, %s]", got, low, high)
		}
		sum += got
	}
	mean := sum / trials
	tolerance := time.Duration(float64(wantCenter) * 0.1)
	if mean < wantCenter-tolerance || mean > wantCenter+tolerance {
		t.Errorf("mean across %d trials = %s, want roughly %s ±%s", trials, mean, wantCenter, tolerance)
	}
}

func TestBackoff_DeterministicWithSeededRNG(t *testing.T) {
	base := time.Second
	maxDelay := time.Minute
	jitter := 0.3

	// Two independent RNGs with the same seed must produce identical
	// sequences — this is what lets Policy tests assert exact delays.
	rng1 := newRNG()
	rng2 := newRNG()
	for attempt := 1; attempt <= 5; attempt++ {
		got1 := outbox.Backoff(attempt, base, maxDelay, jitter, rng1)
		got2 := outbox.Backoff(attempt, base, maxDelay, jitter, rng2)
		if got1 != got2 {
			t.Errorf("attempt=%d not reproducible across seeded RNGs: %s vs %s", attempt, got1, got2)
		}
	}
}

func TestBackoff_ZeroJitterIsDeterministic(t *testing.T) {
	base := time.Second
	maxDelay := time.Minute

	got1 := outbox.Backoff(3, base, maxDelay, 0, nil)
	got2 := outbox.Backoff(3, base, maxDelay, 0, nil)
	if got1 != got2 {
		t.Errorf("Backoff with jitter=0 not deterministic: %s vs %s", got1, got2)
	}
}
