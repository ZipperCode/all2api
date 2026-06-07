package keypool

import (
	"net/http"
	"testing"
)

func TestParseRateLimit(t *testing.T) {
	h := http.Header{}
	h.Set("x-ratelimit-requests-limit", "7500")
	h.Set("x-ratelimit-requests-remaining", "7499")
	h.Set("X-RateLimit-Limit", "10")
	h.Set("X-RateLimit-Remaining", "9")

	snap, ok := ParseRateLimit(h)
	if !ok {
		t.Fatal("ParseRateLimit ok = false, want true")
	}
	if snap.DailyLimit != 7500 || snap.DailyRemaining != 7499 {
		t.Errorf("daily = %d/%d, want 7499/7500", snap.DailyRemaining, snap.DailyLimit)
	}
	if snap.MinuteLimit != 10 || snap.MinuteRemaining != 9 {
		t.Errorf("minute = %d/%d, want 9/10", snap.MinuteRemaining, snap.MinuteLimit)
	}
}

func TestParseRateLimit_MissingHeaders(t *testing.T) {
	h := http.Header{}
	_, ok := ParseRateLimit(h)
	if ok {
		t.Error("ParseRateLimit ok = true on empty headers, want false")
	}
}

func TestParseRateLimit_PartialAndMalformed(t *testing.T) {
	h := http.Header{}
	h.Set("x-ratelimit-requests-remaining", "not-a-number")
	h.Set("x-ratelimit-requests-limit", "100")
	snap, ok := ParseRateLimit(h)
	if !ok {
		t.Fatal("ok = false, want true (limit parseable)")
	}
	if snap.HasDaily {
		t.Error("HasDaily = true, want false (remaining malformed)")
	}
}
