package outbox

import (
	"math"
	"math/rand/v2"
	"time"
)

// Backoff returns the next-attempt delay for a row that just failed.
// Formula: clamp(base * 2^(attempt-1), maxDelay) ± jitter*delay, then
// re-clamp so jitter cannot push the result above maxDelay.
//
// attempt is the count *after* the failed dispatch (i.e. the outbox
// row's `attempts` after Claim's increment). attempt < 1 is treated as
// 1 so the first retry waits `base`. base <= 0 returns 0 immediately —
// "no backoff" semantics rather than silently saturating to maxDelay.
// maxDelay <= 0 likewise returns 0. jitter <= 0 and NaN disable
// jitter; jitter > 1 widens swings and any negative excursion is
// clamped to 0 by the final non-negative guard.
func Backoff(attempt int, base, maxDelay time.Duration, jitter float64) time.Duration {
	if base <= 0 {
		return 0
	}
	if attempt < 1 {
		attempt = 1
	}

	// float64 path so huge `attempt` values can't silently wrap int64;
	// +Inf naturally fails the `<` clamp below and falls through to maxDelay.
	delayF := float64(base) * math.Pow(2, float64(attempt-1))
	delay := maxDelay
	if delayF < float64(maxDelay) {
		delay = time.Duration(delayF)
	}

	if jitter > 0 {
		factor := 1 + jitter*(2*rand.Float64()-1) // [-jitter, +jitter] around 1
		delay = min(time.Duration(float64(delay)*factor), maxDelay)
	}
	return max(delay, 0)
}
