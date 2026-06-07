package auth

import (
	"errors"
	"net/http"
	"testing"
)

func TestAuthorize_DisabledAllowsAnything(t *testing.T) {
	a := New(false, nil)
	req, _ := http.NewRequest(http.MethodGet, "/fixtures", nil)
	if err := a.Authorize(req); err != nil {
		t.Errorf("Authorize (disabled, no header) = %v, want nil", err)
	}
	req.Header.Set("Authorization", "Bearer anything")
	if err := a.Authorize(req); err != nil {
		t.Errorf("Authorize (disabled, any token) = %v, want nil", err)
	}
}

func TestAuthorize_EnabledChecksToken(t *testing.T) {
	a := New(true, []string{"good-token"})
	req, _ := http.NewRequest(http.MethodGet, "/fixtures", nil)

	req.Header.Set("Authorization", "Bearer good-token")
	if err := a.Authorize(req); err != nil {
		t.Errorf("Authorize (valid token) = %v, want nil", err)
	}

	req.Header.Set("Authorization", "Bearer bad-token")
	if err := a.Authorize(req); err == nil {
		t.Error("Authorize (invalid token) = nil, want error")
	}

	req.Header.Del("Authorization")
	if err := a.Authorize(req); err == nil {
		t.Error("Authorize (missing token when enabled) = nil, want error")
	}
}

// TestAuthorize_EnabledEmptyWhitelistRejectsAll 验证 fail-closed 语义：
// 启用认证但白名单为空时，任何 token 都应被拒绝。
func TestAuthorize_EnabledEmptyWhitelistRejectsAll(t *testing.T) {
	a := New(true, nil)
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer any-token")
	if err := a.Authorize(req); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("empty whitelist should reject all, got %v", err)
	}
}

func TestExtractBearer(t *testing.T) {
	cases := []struct {
		header string
		want   string
	}{
		{"Bearer abc", "abc"},
		{"bearer abc", "abc"}, // scheme case-insensitive
		{"BEARER xyz", "xyz"},
		{"", ""},
		{"abc", ""},       // no scheme
		{"Basic abc", ""}, // wrong scheme
		{"Bearer  spaced", "spaced"},
	}
	for _, c := range cases {
		if got := extractBearer(c.header); got != c.want {
			t.Errorf("extractBearer(%q) = %q, want %q", c.header, got, c.want)
		}
	}
}
