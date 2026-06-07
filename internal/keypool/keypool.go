package keypool

import (
	"errors"
	"sync"
	"time"
)

// KeyStatus 是单个 key 的调度状态。
type KeyStatus int

const (
	StatusActive      KeyStatus = iota // 可用
	StatusExhausted                    // 当日额度耗尽
	StatusRateLimited                  // 触发每分钟限速
	StatusError                        // 连续异常
)

func (s KeyStatus) String() string {
	switch s {
	case StatusActive:
		return "active"
	case StatusExhausted:
		return "exhausted"
	case StatusRateLimited:
		return "rate_limited"
	case StatusError:
		return "error"
	default:
		return "unknown"
	}
}

// KeyEntry 是构造池时传入的单个 key 定义。
type KeyEntry struct {
	Label string
	Key   string
}

// Options 控制调度与退避。
type Options struct {
	SwitchThreshold   int
	RateLimitCooldown time.Duration
	ErrorCooldown     time.Duration
	MaxErrorCount     int
}

// keyState 是池内部维护的单个 key 完整状态（不导出，含真实 key）。
type keyState struct {
	label           string
	key             string
	dailyLimit      int
	dailyRemaining  int
	minuteLimit     int
	minuteRemaining int
	lastUpdated     time.Time
	status          KeyStatus
	cooldownUntil   time.Time
	errorCount      int
	lastError       string
}

// KeyStateView 是对外暴露的脱敏快照（用于 /__gateway/status）。
type KeyStateView struct {
	Label           string    `json:"label"`
	MaskedKey       string    `json:"masked_key"`
	Status          string    `json:"status"`
	DailyLimit      int       `json:"daily_limit"`
	DailyRemaining  int       `json:"daily_remaining"`
	MinuteLimit     int       `json:"minute_limit"`
	MinuteRemaining int       `json:"minute_remaining"`
	LastUpdated     time.Time `json:"last_updated"`
	CooldownUntil   time.Time `json:"cooldown_until,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
}

// Pool 是并发安全的 key 池。
type Pool struct {
	mu   sync.RWMutex
	keys []*keyState
	opts Options
	now  func() time.Time // 可注入时钟，便于测试
}

// New 构造一个 key 池，所有 key 初始为 Active。
func New(entries []KeyEntry, opts Options) *Pool {
	keys := make([]*keyState, 0, len(entries))
	for _, e := range entries {
		keys = append(keys, &keyState{
			label:  e.Label,
			key:    e.Key,
			status: StatusActive,
		})
	}
	return &Pool{keys: keys, opts: opts, now: time.Now}
}

// maskKey 返回脱敏后的 key：****+尾4位；长度不足返回 ****。
func maskKey(k string) string {
	if len(k) <= 4 {
		return "****"
	}
	return "****" + k[len(k)-4:]
}

// Snapshot 返回所有 key 的脱敏视图，用于监控端点。
func (p *Pool) Snapshot() []KeyStateView {
	p.mu.RLock()
	defer p.mu.RUnlock()
	views := make([]KeyStateView, 0, len(p.keys))
	for _, k := range p.keys {
		views = append(views, KeyStateView{
			Label:           k.label,
			MaskedKey:       maskKey(k.key),
			Status:          k.status.String(),
			DailyLimit:      k.dailyLimit,
			DailyRemaining:  k.dailyRemaining,
			MinuteLimit:     k.minuteLimit,
			MinuteRemaining: k.minuteRemaining,
			LastUpdated:     k.lastUpdated,
			CooldownUntil:   k.cooldownUntil,
			LastError:       k.lastError,
		})
	}
	return views
}

// ErrNoKeyAvailable 表示当前没有任何可用 key。
var ErrNoKeyAvailable = errors.New("no key available")

// Handle 是一次 Acquire 返回的句柄，标识被选中的 key。
type Handle struct {
	state *keyState
}

// Label 返回句柄对应 key 的标签。
func (h *Handle) Label() string { return h.state.label }

// Key 返回句柄对应的真实 key（仅供注入上游请求，不得记录）。
func (h *Handle) Key() string { return h.state.key }

// Acquire 按顺序耗尽策略返回第一个可用 key 的句柄。
// 选取前会做跨天重置与冷却到期检查。无可用 key 返回 ErrNoKeyAvailable。
func (p *Pool) Acquire() (*Handle, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	for _, k := range p.keys {
		p.refreshLocked(k, now)
		if k.status == StatusActive {
			return &Handle{state: k}, nil
		}
	}
	return nil, ErrNoKeyAvailable
}

// refreshLocked 在持锁状态下根据当前时间复位可恢复的 key。
func (p *Pool) refreshLocked(k *keyState, now time.Time) {
	switch k.status {
	case StatusRateLimited, StatusError:
		if !k.cooldownUntil.IsZero() && now.After(k.cooldownUntil) {
			k.status = StatusActive
			k.errorCount = 0
		}
	case StatusExhausted:
		if !k.lastUpdated.IsZero() && !sameUTCDate(k.lastUpdated, now) {
			k.status = StatusActive
		}
	}
}

func sameUTCDate(a, b time.Time) bool {
	au, bu := a.UTC(), b.UTC()
	return au.Year() == bu.Year() && au.YearDay() == bu.YearDay()
}

// findLocked 按 label 查 keyState，未找到返回 nil。
func (p *Pool) findLocked(label string) *keyState {
	for _, k := range p.keys {
		if k.label == label {
			return k
		}
	}
	return nil
}

// MarkExhausted 将指定 key 标记为当日耗尽。
func (p *Pool) MarkExhausted(label string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if k := p.findLocked(label); k != nil {
		k.status = StatusExhausted
		if k.lastUpdated.IsZero() {
			k.lastUpdated = p.now()
		}
	}
}

// MarkRateLimited 将指定 key 标记为限速并设置冷却。
func (p *Pool) MarkRateLimited(label string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if k := p.findLocked(label); k != nil {
		k.status = StatusRateLimited
		k.cooldownUntil = p.now().Add(p.opts.RateLimitCooldown)
	}
}

// MarkError 累计错误计数，达到上限则标记 Error 并设置冷却。
func (p *Pool) MarkError(label, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if k := p.findLocked(label); k != nil {
		k.errorCount++
		k.lastError = reason
		if p.opts.MaxErrorCount > 0 && k.errorCount >= p.opts.MaxErrorCount {
			k.status = StatusError
			k.cooldownUntil = p.now().Add(p.opts.ErrorCooldown)
		}
	}
}

// MarkKeyInvalid 立即将 key 标记为 Error（用于 401/403 等明确失效）。
func (p *Pool) MarkKeyInvalid(label, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if k := p.findLocked(label); k != nil {
		k.status = StatusError
		k.lastError = reason
		k.cooldownUntil = p.now().Add(p.opts.ErrorCooldown)
	}
}

// UpdateFromUpstream 用上游响应额度快照更新 key 状态。
// 若当日剩余 <= SwitchThreshold，标记为 Exhausted（不影响本次已完成的请求）。
//
// 调用契约：仅应在某个 Acquire 返回的 key 收到其对应上游响应后调用，
// 用该次响应的快照更新自身状态。一次成功响应即视为该 key 工作正常，
// 因此无条件清零累计错误计数。
func (p *Pool) UpdateFromUpstream(label string, snap RateLimitSnapshot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	k := p.findLocked(label)
	if k == nil {
		return
	}
	now := p.now()
	if snap.HasDaily {
		k.dailyLimit = snap.DailyLimit
		k.dailyRemaining = snap.DailyRemaining
	}
	if snap.HasMinute {
		k.minuteLimit = snap.MinuteLimit
		k.minuteRemaining = snap.MinuteRemaining
	}
	k.lastUpdated = now
	k.errorCount = 0 // 成功响应证明 key 正常，无条件清零错误计数
	k.status = StatusActive
	if snap.HasDaily && snap.DailyRemaining <= p.opts.SwitchThreshold {
		k.status = StatusExhausted
	}
}

// defaultRetryAfterSeconds 是无法精确计算恢复时间时的兜底。
const defaultRetryAfterSeconds = 300

// EarliestRecoverySeconds 返回最早一个 key 恢复可用的秒数，用于 503 的 Retry-After。
func (p *Pool) EarliestRecoverySeconds() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := p.now()
	best := -1
	for _, k := range p.keys {
		var secs int
		switch k.status {
		case StatusActive:
			return 0
		case StatusRateLimited, StatusError:
			if k.cooldownUntil.IsZero() {
				secs = defaultRetryAfterSeconds
			} else {
				d := int(k.cooldownUntil.Sub(now).Seconds())
				if d < 1 {
					d = 1
				}
				secs = d
			}
		case StatusExhausted:
			secs = defaultRetryAfterSeconds
		default:
			secs = defaultRetryAfterSeconds
		}
		if best == -1 || secs < best {
			best = secs
		}
	}
	if best < 1 {
		best = defaultRetryAfterSeconds
	}
	return best
}
