package provider

import (
	"net/http"
	"testing"
)

func TestAPISportsProvider(t *testing.T) {
	p, err := New(Config{Type: TypeAPISports})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := http.Header{}
	p.InjectCredential(h, "secret")
	if got := h.Get("x-apisports-key"); got != "secret" {
		t.Errorf("x-apisports-key = %q, want secret", got)
	}

	respHeaders := http.Header{}
	respHeaders.Set("x-ratelimit-requests-limit", "100")
	respHeaders.Set("x-ratelimit-requests-remaining", "7")
	snap := p.ParseRateLimit(respHeaders)
	if !snap.HasDaily || snap.DailyLimit != 100 || snap.DailyRemaining != 7 {
		t.Errorf("snapshot = %+v, want daily 100/7", snap)
	}
}

func TestGenericProvider(t *testing.T) {
	p, err := New(Config{
		Type: TypeGenericHTTP,
		Auth: HeaderAuthConfig{Header: "Authorization", Prefix: "Bearer"},
		RateLimit: RateLimitConfig{
			DailyLimitHeader:     "X-Daily-Limit",
			DailyRemainingHeader: "X-Daily-Remaining",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := http.Header{}
	p.InjectCredential(h, "secret")
	if got := h.Get("Authorization"); got != "Bearer secret" {
		t.Errorf("Authorization = %q, want Bearer secret", got)
	}

	respHeaders := http.Header{}
	respHeaders.Set("X-Daily-Limit", "50")
	respHeaders.Set("X-Daily-Remaining", "49")
	snap := p.ParseRateLimit(respHeaders)
	if !snap.HasDaily || snap.DailyRemaining != 49 {
		t.Errorf("snapshot = %+v, want daily remaining 49", snap)
	}
}

func TestGenericProviderRequiresHeader(t *testing.T) {
	_, err := New(Config{Type: TypeGenericHTTP})
	if err == nil {
		t.Fatal("New generic without auth.header = nil error, want error")
	}
}
