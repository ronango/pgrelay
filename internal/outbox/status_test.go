package outbox

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeLastError_StripsControlBytes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "503 upstream timeout", "503 upstream timeout"},
		{"keeps_tab", "code\t503", "code\t503"},
		{"keeps_newline", "line1\nline2", "line1\nline2"},
		{"strips_cr", "before\rafter", "beforeafter"},
		{"strips_nul", "before\x00after", "beforeafter"},
		{"strips_esc", "\x1b[31mred\x1b[0m", "[31mred[0m"},
		{"strips_del", "before\x7fafter", "beforeafter"},
		{"strips_low_control", "\x01\x02\x03text", "text"},
		{"keeps_utf8", "ошибка 503", "ошибка 503"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeLastError(tc.in); got != tc.want {
				t.Errorf("sanitizeLastError(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeLastError_TruncatesAtCap(t *testing.T) {
	in := strings.Repeat("a", maxLastErrorBytes+500)
	got := sanitizeLastError(in)
	if len(got) != maxLastErrorBytes {
		t.Errorf("len = %d, want %d", len(got), maxLastErrorBytes)
	}
}

func TestSanitizeLastError_BoundedOnAdversarialUTF8(t *testing.T) {
	// 0xE5 alone is a UTF-8 lead byte without continuation; strings.Map
	// decodes each as U+FFFD (3 bytes encoded). Without the post-Map
	// re-cap the cap would be busted 3x.
	in := strings.Repeat("\xe5", maxLastErrorBytes+100)
	got := sanitizeLastError(in)
	if len(got) > maxLastErrorBytes {
		t.Errorf("len = %d, want <= %d (adversarial UTF-8 inflated past cap)", len(got), maxLastErrorBytes)
	}
}

func TestSanitizeLastError_AllContinuationBytesReturnsEmpty(t *testing.T) {
	// Pathological input — every byte is a UTF-8 continuation byte
	// (0x80). truncateAtRuneBoundary walks all the way to end=0.
	in := strings.Repeat("\x80", maxLastErrorBytes+10)
	if got := sanitizeLastError(in); got != "" {
		t.Errorf("len = %d, want 0 (no rune starts to truncate at)", len(got))
	}
}

func TestSanitizeLastError_DoesNotSplitMultibyteRune(t *testing.T) {
	// 'ё' is 2 bytes (0xD1 0x91). Position it so the cap falls between
	// its two bytes — the truncator must back up to a rune boundary.
	prefix := strings.Repeat("a", maxLastErrorBytes-1)
	in := prefix + "ё" + "tail"
	got := sanitizeLastError(in)
	if !utf8.ValidString(got) {
		t.Errorf("output is not valid UTF-8: %q", got)
	}
}
