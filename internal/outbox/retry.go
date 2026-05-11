package outbox

import (
	"math"
	"math/rand/v2"
	"time"
)

// Backoff returns the next-attempt delay: clamp(base * 2^(attempt-1),
// maxDelay) ± jitter*delay, re-clamped so jitter can't exceed maxDelay.
//
// Edge cases (semantic, not derivable from the formula):
//   - attempt < 1 treated as 1 (first retry waits `base`).
//   - base <= 0 or maxDelay <= 0 returns 0 ("no backoff", not saturate).
//   - jitter <= 0 or NaN disables jitter; jitter > 1 widens swings and
//     any negative excursion is clamped to 0.
//   - rng may be nil when jitter <= 0; required otherwise.
func Backoff(attempt int, base, maxDelay time.Duration, jitter float64, rng *rand.Rand) time.Duration {
	if base <= 0 {
		return 0
	}
	if attempt < 1 {
		attempt = 1
	}

	// float64 keeps huge `attempt` from wrapping int64; +Inf fails the
	// `<` below and falls through to maxDelay.
	delayF := float64(base) * math.Pow(2, float64(attempt-1))
	delay := maxDelay
	if delayF < float64(maxDelay) {
		delay = time.Duration(delayF)
	}

	if jitter > 0 {
		// 2x-1 maps Float64's [0,1) to [-1,1).
		factor := 1 + jitter*(2*rng.Float64()-1)
		delay = min(time.Duration(float64(delay)*factor), maxDelay)
	}
	return max(delay, 0)
}
