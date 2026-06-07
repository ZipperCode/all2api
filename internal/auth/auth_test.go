package auth

import (
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
