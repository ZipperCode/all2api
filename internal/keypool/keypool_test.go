package keypool

import (
	"testing"
	"time"
)

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func newTestPool(t *testing.T) *Pool {
	t.Helper()
	return New([]KeyEntry{
		{Label: "key-1", Key: "secret-abcd1"},
		{Label: "key-2", Key: "secret-wxyz9"},
	}, Options{
		SwitchThreshold:   1,
		RateLimitCooldown: 60 * time.Second,
		ErrorCooldown:     30 * time.Second,
		MaxErrorCount:     3,
	})
}

func TestNew_InitialStateActive(t *testing.T) {
	p := newTestPool(t)
	states := p.Snapshot()
	if len(states) != 2 {
		t.Fatalf("len(states) = %d, want 2", len(states))
	}
	for _, s := range states {
		if s.Status != "active" {
			t.Errorf("key %s initial status = %v, want active", s.Label, s.Status)
		}
	}
}

func TestMaskedKey(t *testing.T) {
	got := maskKey("secret-abcd1")
	if got != "****bcd1" {
		t.Errorf("maskKey = %q, want ****bcd1", got)
	}
	if maskKey("ab") != "****" {
		t.Errorf("maskKey short = %q, want ****", maskKey("ab"))
	}
}

func TestSnapshot_DoesNotLeakFullKey(t *testing.T) {
	p := newTestPool(t)
	for _, s := range p.Snapshot() {
		if s.MaskedKey == "secret-abcd1" || s.MaskedKey == "secret-wxyz9" {
			t.Errorf("Snapshot leaked full key: %q", s.MaskedKey)
		}
	}
}

func TestAcquire_SequentialExhaustion(t *testing.T) {
	p := newTestPool(t)
	h, err := p.Acquire()
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if h.Label() != "key-1" {
		t.Errorf("first Acquire = %s, want key-1", h.Label())
	}
	p.MarkExhausted("key-1")
	h2, err := p.Acquire()
	if err != nil {
		t.Fatalf("Acquire after exhaust: %v", err)
	}
	if h2.Label() != "key-2" {
		t.Errorf("after key-1 exhausted, Acquire = %s, want key-2", h2.Label())
	}
}

func TestAcquire_AllUnavailable(t *testing.T) {
	p := newTestPool(t)
	p.MarkExhausted("key-1")
	p.MarkExhausted("key-2")
	_, err := p.Acquire()
	if err == nil {
		t.Fatal("Acquire returned nil error when all keys unavailable")
	}
}

func TestAcquire_SkipsRateLimitedUntilCooldown(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	p := newTestPool(t)
	p.now = fixedClock(now)

	p.MarkRateLimited("key-1")
	h, err := p.Acquire()
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if h.Label() != "key-2" {
		t.Errorf("Acquire = %s, want key-2 (key-1 rate limited)", h.Label())
	}

	p.now = fixedClock(now.Add(61 * time.Second))
	h2, err := p.Acquire()
	if err != nil {
		t.Fatalf("Acquire after cooldown: %v", err)
	}
	if h2.Label() != "key-1" {
		t.Errorf("after cooldown Acquire = %s, want key-1", h2.Label())
	}
}

func TestAcquire_CrossDayResetsExhausted(t *testing.T) {
	day1 := time.Date(2026, 6, 7, 23, 0, 0, 0, time.UTC)
	p := newTestPool(t)
	p.now = fixedClock(day1)
	p.UpdateFromUpstream("key-1", RateLimitSnapshot{DailyLimit: 100, DailyRemaining: 0, HasDaily: true})
	if got := statusOf(t, p, "key-1"); got != "exhausted" {
		t.Fatalf("key-1 status = %s, want exhausted", got)
	}
	day2 := time.Date(2026, 6, 8, 1, 0, 0, 0, time.UTC)
	p.now = fixedClock(day2)
	h, err := p.Acquire()
	if err != nil {
		t.Fatalf("Acquire day2: %v", err)
	}
	if h.Label() != "key-1" {
		t.Errorf("day2 Acquire = %s, want key-1 (cross-day reset)", h.Label())
	}
}

func statusOf(t *testing.T, p *Pool, label string) string {
	t.Helper()
	for _, s := range p.Snapshot() {
		if s.Label == label {
			return s.Status
		}
	}
	t.Fatalf("label %s not found", label)
	return ""
}

func TestEarliestRecoverySeconds(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	p := newTestPool(t)
	p.now = fixedClock(now)
	p.MarkRateLimited("key-1")
	p.MarkExhausted("key-2")

	secs := p.EarliestRecoverySeconds()
	if secs != 60 {
		t.Errorf("EarliestRecoverySeconds = %d, want 60", secs)
	}
}

func TestEarliestRecoverySeconds_AllExhausted(t *testing.T) {
	p := newTestPool(t)
	p.MarkExhausted("key-1")
	p.MarkExhausted("key-2")
	secs := p.EarliestRecoverySeconds()
	if secs <= 0 {
		t.Errorf("EarliestRecoverySeconds = %d, want > 0 fallback", secs)
	}
}

func TestMarkError_AccumulatesToThreshold(t *testing.T) {
	p := newTestPool(t)
	// MaxErrorCount=3: first two errors stay Active, third trips Error
	p.MarkError("key-1", "boom")
	if statusOf(t, p, "key-1") != "active" {
		t.Errorf("after 1 error, want active")
	}
	p.MarkError("key-1", "boom")
	p.MarkError("key-1", "boom")
	if statusOf(t, p, "key-1") != "error" {
		t.Errorf("after 3 errors, want error")
	}
}
