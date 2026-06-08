// Package provider defines upstream platform behavior that differs between APIs.
package provider

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"all2api/internal/keypool"
)

const (
	TypeAPISports   = "api_sports"
	TypeGenericHTTP = "generic_http"
)

// Outcome is a provider-specific classification of an upstream response.
type Outcome int

const (
	OutcomeSuccess Outcome = iota
	OutcomeRateLimited
	OutcomeKeyInvalid
	OutcomeServerError
	OutcomeClientError
)

// HeaderAuthConfig configures header-based credential injection.
type HeaderAuthConfig struct {
	Header string
	Prefix string
}

// RateLimitConfig configures response headers used to update key state.
type RateLimitConfig struct {
	DailyLimitHeader      string
	DailyRemainingHeader  string
	MinuteLimitHeader     string
	MinuteRemainingHeader string
}

// Config configures a provider instance.
type Config struct {
	Type      string
	Auth      HeaderAuthConfig
	RateLimit RateLimitConfig
}

// Provider describes upstream behavior needed by the proxy pipeline.
type Provider interface {
	Type() string
	CredentialHeader() string
	InjectCredential(h http.Header, credential string)
	ParseRateLimit(h http.Header) keypool.RateLimitSnapshot
	ClassifyStatus(status int) Outcome
}

// New constructs a provider from config.
func New(cfg Config) (Provider, error) {
	switch cfg.Type {
	case TypeAPISports:
		return newHeaderProvider(TypeAPISports, HeaderAuthConfig{Header: "x-apisports-key"}, RateLimitConfig{
			DailyLimitHeader:      "x-ratelimit-requests-limit",
			DailyRemainingHeader:  "x-ratelimit-requests-remaining",
			MinuteLimitHeader:     "X-RateLimit-Limit",
			MinuteRemainingHeader: "X-RateLimit-Remaining",
		})
	case TypeGenericHTTP:
		return newHeaderProvider(TypeGenericHTTP, cfg.Auth, cfg.RateLimit)
	default:
		return nil, fmt.Errorf("unknown provider type %q", cfg.Type)
	}
}

type headerProvider struct {
	typ       string
	auth      HeaderAuthConfig
	rateLimit RateLimitConfig
}

func newHeaderProvider(typ string, auth HeaderAuthConfig, rateLimit RateLimitConfig) (*headerProvider, error) {
	auth.Header = strings.TrimSpace(auth.Header)
	if auth.Header == "" {
		return nil, fmt.Errorf("provider %q requires auth.header", typ)
	}
	return &headerProvider{typ: typ, auth: auth, rateLimit: rateLimit}, nil
}

func (p *headerProvider) Type() string { return p.typ }

func (p *headerProvider) CredentialHeader() string {
	return http.CanonicalHeaderKey(p.auth.Header)
}

func (p *headerProvider) InjectCredential(h http.Header, credential string) {
	h.Set(p.CredentialHeader(), withPrefix(p.auth.Prefix, credential))
}

func (p *headerProvider) ParseRateLimit(h http.Header) keypool.RateLimitSnapshot {
	return parseRateLimit(h, p.rateLimit)
}

func (p *headerProvider) ClassifyStatus(status int) Outcome {
	switch {
	case status >= 200 && status < 300:
		return OutcomeSuccess
	case status == http.StatusTooManyRequests:
		return OutcomeRateLimited
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return OutcomeKeyInvalid
	case status >= 500:
		return OutcomeServerError
	default:
		return OutcomeClientError
	}
}

func withPrefix(prefix, credential string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return credential
	}
	return prefix + " " + credential
}

func parseRateLimit(h http.Header, cfg RateLimitConfig) keypool.RateLimitSnapshot {
	var snap keypool.RateLimitSnapshot

	dl, dlOK := atoiConfiguredHeader(h, cfg.DailyLimitHeader)
	dr, drOK := atoiConfiguredHeader(h, cfg.DailyRemainingHeader)
	if dlOK {
		snap.DailyLimit = dl
	}
	if drOK {
		snap.DailyRemaining = dr
	}
	snap.HasDaily = dlOK && drOK

	ml, mlOK := atoiConfiguredHeader(h, cfg.MinuteLimitHeader)
	mr, mrOK := atoiConfiguredHeader(h, cfg.MinuteRemainingHeader)
	if mlOK {
		snap.MinuteLimit = ml
	}
	if mrOK {
		snap.MinuteRemaining = mr
	}
	snap.HasMinute = mlOK && mrOK

	return snap
}

func atoiConfiguredHeader(h http.Header, name string) (int, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, false
	}
	v := h.Get(name)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}
