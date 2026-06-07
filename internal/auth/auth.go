// Package auth 提供下游客户端认证钩子。初版不启用（直通），
// 启用后校验 Authorization: Bearer <token> 是否在白名单内。
package auth

import (
	"errors"
	"net/http"
	"strings"
)

// ErrUnauthorized 表示客户端凭证缺失或不在白名单内。
var ErrUnauthorized = errors.New("unauthorized")

// Authenticator 校验下游请求的 Authorization 头。
type Authenticator struct {
	enabled bool
	tokens  map[string]struct{}
}

// New 构造认证器。enabled=false 时一律放行。
func New(enabled bool, tokens []string) *Authenticator {
	set := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		set[t] = struct{}{}
	}
	return &Authenticator{enabled: enabled, tokens: set}
}

// Authorize 校验请求；未启用直接返回 nil。
func (a *Authenticator) Authorize(r *http.Request) error {
	if !a.enabled {
		return nil
	}
	token := extractBearer(r.Header.Get("Authorization"))
	if token == "" {
		return ErrUnauthorized
	}
	if _, ok := a.tokens[token]; !ok {
		return ErrUnauthorized
	}
	return nil
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
