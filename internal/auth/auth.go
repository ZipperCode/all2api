// Package auth 提供下游客户端认证钩子。初版不启用（直通），
// 启用后校验客户端凭证是否在白名单内。客户端凭证可放在两个位置：
// Authorization: Bearer <token>，或 x-apisports-key: <token>（与上游同名，便于下游零改造）。
package auth

import (
	"errors"
	"net/http"
	"strings"
)

// ErrUnauthorized 表示客户端凭证缺失或不在白名单内。
var ErrUnauthorized = errors.New("unauthorized")

// Authenticator 校验下游请求的客户端凭证。
type Authenticator struct {
	enabled     bool
	tokens      map[string]struct{}
	tokenHeader string
}

// New 构造认证器。enabled=false 时一律放行。
func New(enabled bool, tokens []string) *Authenticator {
	return NewWithHeader(enabled, tokens, "")
}

// NewWithHeader 构造认证器，并允许按当前站点的官方认证头提取客户端凭证。
func NewWithHeader(enabled bool, tokens []string, tokenHeader string) *Authenticator {
	set := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if t != "" {
			set[t] = struct{}{}
		}
	}
	return &Authenticator{enabled: enabled, tokens: set, tokenHeader: strings.TrimSpace(tokenHeader)}
}

// Enabled reports whether client authentication is enforced.
func (a *Authenticator) Enabled() bool {
	if a == nil {
		return false
	}
	return a.enabled
}

// Tokens returns the configured client token whitelist.
func (a *Authenticator) Tokens() []string {
	if a == nil {
		return nil
	}
	tokens := make([]string, 0, len(a.tokens))
	for token := range a.tokens {
		tokens = append(tokens, token)
	}
	return tokens
}

// Authorize 校验请求；未启用直接返回 nil。
func (a *Authenticator) Authorize(r *http.Request) error {
	if a == nil || !a.enabled {
		return nil
	}
	token := a.extractClientToken(r)
	if token == "" {
		return ErrUnauthorized
	}
	if _, ok := a.tokens[token]; !ok {
		return ErrUnauthorized
	}
	return nil
}

func (a *Authenticator) extractClientToken(r *http.Request) string {
	if a.tokenHeader != "" {
		return stripConfiguredPrefix(a.tokenHeader, strings.TrimSpace(r.Header.Get(a.tokenHeader)))
	}
	return extractClientToken(r)
}

// extractClientToken 从请求中提取客户端凭证，依次尝试两个位置：
//  1. Authorization: Bearer <token>
//  2. x-apisports-key: <token>（与上游同名头，下游可零改造沿用）
//
// 先取到非空者即返回。
func extractClientToken(r *http.Request) string {
	if t := extractBearer(r.Header.Get("Authorization")); t != "" {
		return t
	}
	return strings.TrimSpace(r.Header.Get("x-apisports-key"))
}

func stripConfiguredPrefix(header, value string) string {
	if strings.EqualFold(strings.TrimSpace(header), "Authorization") {
		if token := extractBearer(value); token != "" {
			return token
		}
	}
	return strings.TrimSpace(value)
}

// extractBearer 从 "Bearer <token>" 提取 token，scheme 大小写不敏感。
func extractBearer(header string) string {
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}
