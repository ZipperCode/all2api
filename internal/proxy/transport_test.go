package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"all2api/internal/provider"
)

func TestClassifyOutcome(t *testing.T) {
	cases := []struct {
		status int
		want   outcomeKind
	}{
		{200, outcomeSuccess},
		{204, outcomeSuccess},
		{429, outcomeRateLimited},
		{401, outcomeKeyInvalid},
		{403, outcomeKeyInvalid},
		{500, outcomeServerError},
		{502, outcomeServerError},
		{400, outcomeClientError},
		{404, outcomeClientError},
	}
	for _, c := range cases {
		if got := classifyStatus(c.status); got != c.want {
			t.Errorf("classifyStatus(%d) = %v, want %v", c.status, got, c.want)
		}
	}
}

func TestDoUpstream_InjectsKeyAndStripsAuthorization(t *testing.T) {
	var gotAPIKey, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("x-apisports-key")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("x-ratelimit-requests-limit", "100")
		w.Header().Set("x-ratelimit-requests-remaining", "99")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	tr := newTransport(srv.URL, 5*time.Second, mustAPISportsProvider())

	clientReq, _ := http.NewRequest(http.MethodGet, "http://gateway/fixtures?live=all", nil)
	clientReq.Header.Set("Authorization", "Bearer client-token")

	res := tr.do(clientReq, "real-secret")
	if res.err != nil {
		t.Fatalf("do err: %v", res.err)
	}
	if gotAPIKey != "real-secret" {
		t.Errorf("upstream x-apisports-key = %q, want real-secret", gotAPIKey)
	}
	if gotAuth != "" {
		t.Errorf("upstream received Authorization = %q, want empty (stripped)", gotAuth)
	}
	if res.kind != outcomeSuccess {
		t.Errorf("kind = %v, want success", res.kind)
	}
	if !res.snapshot.HasDaily || res.snapshot.DailyRemaining != 99 {
		t.Errorf("snapshot = %+v, want daily 99/100", res.snapshot)
	}
	if string(res.body) != `{"ok":true}` {
		t.Errorf("body = %q", string(res.body))
	}
}

func TestDoUpstream_PreservesQueryAndPath(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.WriteHeader(200)
	}))
	defer srv.Close()

	tr := newTransport(srv.URL, 5*time.Second, mustAPISportsProvider())
	clientReq, _ := http.NewRequest(http.MethodGet, "http://gateway/players?team=33&season=2024", nil)
	res := tr.do(clientReq, "k")
	if res.err != nil {
		t.Fatalf("do: %v", res.err)
	}
	if gotPath != "/players" {
		t.Errorf("path = %q, want /players", gotPath)
	}
	if gotQuery != "team=33&season=2024" {
		t.Errorf("query = %q, want team=33&season=2024", gotQuery)
	}
}

func TestDoUpstream_GenericHeaderProvider(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("X-Daily-Limit", "50")
		w.Header().Set("X-Daily-Remaining", "49")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	p, err := provider.New(provider.Config{
		Type: provider.TypeGenericHTTP,
		Auth: provider.HeaderAuthConfig{Header: "Authorization", Prefix: "Bearer"},
		RateLimit: provider.RateLimitConfig{
			DailyLimitHeader:     "X-Daily-Limit",
			DailyRemainingHeader: "X-Daily-Remaining",
		},
	})
	if err != nil {
		t.Fatalf("provider.New: %v", err)
	}
	tr := newTransport(srv.URL, 5*time.Second, p)

	clientReq, _ := http.NewRequest(http.MethodGet, "http://gateway/forecast", nil)
	clientReq.Header.Set("Authorization", "Bearer downstream-token")
	res := tr.do(clientReq, "upstream-secret")

	if res.err != nil {
		t.Fatalf("do err: %v", res.err)
	}
	if gotAuth != "Bearer upstream-secret" {
		t.Errorf("upstream Authorization = %q, want Bearer upstream-secret", gotAuth)
	}
	if !res.snapshot.HasDaily || res.snapshot.DailyRemaining != 49 {
		t.Errorf("snapshot = %+v, want daily remaining 49", res.snapshot)
	}
}
