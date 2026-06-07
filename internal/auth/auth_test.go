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

// TestAuthorize_AcceptsXApisportsKeyHeader 验证客户端凭证也可放在 x-apisports-key 头。
func TestAuthorize_AcceptsXApisportsKeyHeader(t *testing.T) {
	a := New(true, []string{"good-token"})

	// 仅用 x-apisports-key 头（无 Authorization）
	req, _ := http.NewRequest(http.MethodGet, "/fixtures", nil)
	req.Header.Set("x-apisports-key", "good-token")
	if err := a.Authorize(req); err != nil {
		t.Errorf("Authorize (valid x-apisports-key) = %v, want nil", err)
	}

	req2, _ := http.NewRequest(http.MethodGet, "/fixtures", nil)
	req2.Header.Set("x-apisports-key", "bad-token")
	if err := a.Authorize(req2); err == nil {
		t.Error("Authorize (invalid x-apisports-key) = nil, want error")
	}
}

// TestAuthorize_AuthorizationTakesPrecedence 验证 Authorization 优先于 x-apisports-key。
func TestAuthorize_AuthorizationTakesPrecedence(t *testing.T) {
	a := New(true, []string{"auth-token"})
	req, _ := http.NewRequest(http.MethodGet, "/fixtures", nil)
	req.Header.Set("Authorization", "Bearer auth-token") // 有效
	req.Header.Set("x-apisports-key", "wrong")           // 无效但应被忽略
	if err := a.Authorize(req); err != nil {
		t.Errorf("Authorize (valid Authorization, ignore x-apisports-key) = %v, want nil", err)
	}
}

func TestExtractClientToken(t *testing.T) {
	// Authorization 优先
	r1, _ := http.NewRequest(http.MethodGet, "/", nil)
	r1.Header.Set("Authorization", "Bearer aaa")
	r1.Header.Set("x-apisports-key", "bbb")
	if got := extractClientToken(r1); got != "aaa" {
		t.Errorf("extractClientToken (both) = %q, want aaa", got)
	}
	// 回退到 x-apisports-key
	r2, _ := http.NewRequest(http.MethodGet, "/", nil)
	r2.Header.Set("x-apisports-key", "bbb")
	if got := extractClientToken(r2); got != "bbb" {
		t.Errorf("extractClientToken (x-apisports-key only) = %q, want bbb", got)
	}
	// 都无
	r3, _ := http.NewRequest(http.MethodGet, "/", nil)
	if got := extractClientToken(r3); got != "" {
		t.Errorf("extractClientToken (none) = %q, want empty", got)
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
