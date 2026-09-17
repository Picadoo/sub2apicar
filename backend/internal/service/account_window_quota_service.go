package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// 官方窗口类型常量。
const (
	WindowType5h = "5h"
	WindowType7d = "7d"

	// AccountWindowForceUnattributedExtraKey 标记账号官方额度还会被中转站外部来源消耗。
	// 启用后，官方增量全部记为未归因，绝不使用中转站 usage log 将其分配给成员。
	AccountWindowForceUnattributedExtraKey = "window_quota_force_unattributed"
	// Shared pool mode suspends personal caps without changing membership or usage.
	AccountWindowSharedPoolModeExtraKey = "window_quota_shared_pool_mode"

	// v4 checkpoint 持久化官方窗口、前向 usage_log 游标和待结算美元 basis。
	// 成员历史 attributed_percent 只增量推进，刷新或迟到日志不得触发整窗重分。
	accountWindowAttributionBasis = "usage_log_incremental_v4"
	accountWindowAttributionMode  = "official_delta_cost_basis"

	// DefaultAccountWindowLimitPercent 是单用户在某账号某窗口可占用的官方利用率百分点默认上限。
	// 4 人共享：4×23%=92%，留 ~8% 安全缓冲，避免占满官方窗口触发风控。
	DefaultAccountWindowLimitPercent = 23.0

	// DefaultAccountWindowTotalCeilingPercent 是某账号某窗口下「所有用户 limit 之和」的默认总额上限。
	// 4×23%=92%，留 ~8% 安全缓冲。管理端设置单用户 limit 时不得令该窗口各用户之和超过此上限。
	// 运行时可通过 SetTotalCeiling 覆盖（存 Redis，无 TTL），缺省回落到本默认值。
	DefaultAccountWindowTotalCeilingPercent = 92.0

	// DefaultAccountSeats 是默认车位数（共享同一账号的人数）。人均默认上限 = ceiling/seats。
	// 4 人车：92/4=23%。运行时可通过 SetSeats 覆盖（存 Redis），换成 3/5/8 人车无需改代码。
	DefaultAccountSeats = 4

	// accountWindowCheckpointTTLSeconds 是成员均分归因检查点的存活时间（8 天 > 最长 7d 窗口）。
	accountWindowCheckpointTTLSeconds = 8 * 24 * 60 * 60

	window5hLengthSeconds = 5 * 60 * 60
	window7dLengthSeconds = 7 * 24 * 60 * 60

	// AccountWindowBoundaryTolerance absorbs upstream reset-after jitter without mistaking it for a new window.
	// Real 5h/7d rollovers move the boundary by the full window length, while observed OpenAI drift can span several minutes.
	AccountWindowBoundaryTolerance = 15 * time.Minute
)

// ErrAccountWindowCeilingExceeded 表示设置某用户 limit 会令该窗口所有用户之和超过总额上限。
var ErrAccountWindowCeilingExceeded = errors.New("account window total ceiling exceeded")

// ErrAccountWindowCeilingBelowConfigured 表示新 ceiling 低于现有账号的 configured sum，会制造新的超配不一致。
var ErrAccountWindowCeilingBelowConfigured = errors.New("account window ceiling is below existing configured sum")

// ErrAccountWindowNoActiveMembers 表示指定账号至少一个窗口没有可均分的活跃成员。
var ErrAccountWindowNoActiveMembers = errors.New("account window has no active members")

// ErrAccountWindowInvalidMembers 表示显式成员列表为空、含无效用户或账号不存在。
var ErrAccountWindowInvalidMembers = errors.New("account window member selection is invalid")

// ErrAccountWindowDonationInUse 表示用户试图收回已经被其他成员实际借用的救急池额度。
var ErrAccountWindowDonationInUse = errors.New("donated account window quota is already in use")

// ErrAccountWindowNotMember 表示用户不是该拼车账号指定窗口的活跃成员。
var ErrAccountWindowNotMember = errors.New("user is not an active account window member")

// AccountWindowDonationInUseError 带出当前窗口允许的最低捐赠比例，供 API 和前端提示。
type AccountWindowDonationInUseError struct {
	MinimumFraction float64
}

func (e *AccountWindowDonationInUseError) Error() string {
	return fmt.Sprintf("%s: minimum donation is %.2f%% until the window resets", ErrAccountWindowDonationInUse.Error(), e.MinimumFraction*100)
}

func (e *AccountWindowDonationInUseError) Unwrap() error {
	return ErrAccountWindowDonationInUse
}

// accountWindowTypes 是需要分摊/校验的全部官方窗口。
var accountWindowTypes = []string{WindowType5h, WindowType7d}

// UserAccountWindowQuotaRecord 是 user × account × window 维度配额台账的传输结构体。
type UserAccountWindowQuotaRecord struct {
	SharedPoolMode    bool
	UserID            int64
	AccountID         int64
	WindowType        string
	LimitPercent      float64
	AttributedPercent float64
	WindowResetAt     *time.Time
	// DonatePoolFraction 是用户愿意捐入 5h 救急池的比例（占自己 5h 上限），∈[0,1]；仅 5h 行有意义。
	DonatePoolFraction float64
}

// AdminWindowQuotaOverviewRow 是管理端总览用的配额行（带用户邮箱/用户名）。
type AdminWindowQuotaOverviewRow struct {
	SharedPoolMode        bool
	UserID                int64
	Email                 string
	Username              string
	AccountID             int64
	WindowType            string
	LimitPercent          float64
	AttributedPercent     float64
	WindowResetAt         *time.Time
	DonatePoolFraction    float64
	EffectiveLimitPercent float64 // 含救急池增量/捐赠自留后的当前有效上限
	PoolAvailablePercent  float64 // 该账号该窗口救急池当前可借总额
}

// AdminWindowQuotaSummary 是管理端按账号、窗口聚合的配置/用量诊断。
type AdminWindowQuotaSummary struct {
	SharedPoolMode       bool
	AccountID            int64
	WindowType           string
	MemberCount          int
	ConfiguredSumPercent float64
	AttributedSumPercent float64
	OfficialUsedPercent  *float64 // OpenAI 官方账号窗口快照；缺失时为 nil，绝不从成员归因反推
	UnattributedPercent  *float64 // 官方总量减当前成员已归因；缺失官方快照时为 nil
	CeilingPercent       float64
	Overallocated        bool
}

// AccountWindowEqualizationResult 描述一次按活跃成员均分后的窗口结果。
type AccountWindowEqualizationResult struct {
	WindowType        string
	ActiveMemberCount int
	SharePercent      float64
	CeilingPercent    float64
}

// AccountWindowReset 标识一个待重置的 (账号, 窗口) 组合。
type AccountWindowReset struct {
	AccountID  int64
	WindowType string
}

// UserAccountWindowQuotaRepository 定义 user × account × window 配额台账的数据访问接口。
type UserAccountWindowQuotaRepository interface {
	// ResetWindowForAccount 把某账号某窗口下所有活跃用户的 attributed_percent 清零，并刷新 window_reset_at。
	// 仅 5h 重置清零 donate_pool_fraction（5h 逐窗口重新决定）；7d 捐赠永久保留，绝不自动清。
	// ResetWindowForAccountIfDue clears a window only if its persisted reset boundary
	// is still due at dueAt; false means a newer official window superseded the scan.
	ResetWindowForAccountIfDue(ctx context.Context, accountID int64, window string, dueAt time.Time) (bool, error)
	// GetByUserAccountWindow 查询单条配额；未找到返回 (nil, nil)。
	GetByUserAccountWindow(ctx context.Context, userID, accountID int64, window string) (*UserAccountWindowQuotaRecord, error)
	// ListByUser 返回用户在所有账号所有窗口的活跃配额。
	ListByUser(ctx context.Context, userID int64) ([]UserAccountWindowQuotaRecord, error)
	// ListByAccount 返回某账号下所有用户所有窗口的活跃配额。
	ListByAccount(ctx context.Context, accountID int64) ([]UserAccountWindowQuotaRecord, error)
	// ListDueResets 返回 window_reset_at 已到期且仍有用量的 (账号, 窗口) 去重列表。
	ListDueResets(ctx context.Context, now time.Time) ([]AccountWindowReset, error)
	// SetLimitForUserAccount 设置（或新建）某 (user, account, window) 的 limit_percent。
	SetLimitForUserAccount(ctx context.Context, userID, accountID int64, window string, limitPercent float64) error
	// SetDonatePoolFraction 设置（或新建）某 (user, account, window) 的救急池捐赠比例（∈[0,1]）。
	// 新建行时 limit_percent 取 defaultLimit（人均默认 = ceiling/seats）。
	SetDonatePoolFraction(ctx context.Context, userID, accountID int64, window string, fraction, defaultLimit float64) error
	// ApplyWindowSharesFromCheckpoint 在同一数据库事务内选出持久化 checkpoint 胜者，
	// 并按账号归因模式结算官方增量；外部使用账号的增量必须全部保持未归因。
	ApplyWindowSharesFromCheckpoint(ctx context.Context, accountID int64, window string, candidate AccountWindowAttributionCheckpoint, resetAt *time.Time) (AccountWindowAttributionCheckpoint, bool, error)
	// ListAllWithUser 返回所有活跃配额行（带用户邮箱/用户名），供管理端总览。
	ListAllWithUser(ctx context.Context) ([]AdminWindowQuotaOverviewRow, error)
	// GetMaxConfiguredSumForWindow 返回指定窗口中各账号 configured sum 的最大值。
	GetMaxConfiguredSumForWindow(ctx context.Context, window string) (float64, error)
	// EqualizeActiveMemberLimits 在单个事务中把指定账号 5h/7d 的 limit 分别均分给各窗口活跃成员。
	EqualizeActiveMemberLimits(ctx context.Context, accountID int64, ceiling5h, ceiling7d float64) ([]AccountWindowEqualizationResult, error)
	// SyncAccountMembers 只同步账号成员；保留 retained 状态，并在同一账号行锁事务内冻结成员变更前的增量成本边界。
	SyncAccountMembers(ctx context.Context, accountID int64, userIDs []int64, ceiling5h, ceiling7d float64) ([]AccountWindowEqualizationResult, error)
}

// accountWindowAtomicLimitSetter 在账号行锁事务内重查 configured sum 并写入 limit。
// 生产仓储实现该能力；保留旧接口方法仅用于兼容轻量测试桩。
type accountWindowAtomicLimitSetter interface {
	SetLimitForUserAccountWithinCeiling(ctx context.Context, userID, accountID int64, window string, limitPercent, ceilingPercent float64) error
}

// sharedAccountIDReader / peerSharedAccountMemberReader 是新拼车账号初始化所需的可选仓储能力。
// 保持主接口兼容现有测试桩；生产仓储实现这两个接口。
type sharedAccountIDReader interface {
	ListSharedAccountIDs(ctx context.Context) ([]int64, error)
}

type peerSharedAccountMemberReader interface {
	ListPeerSharedAccountMemberUserIDs(ctx context.Context, accountID int64) ([]int64, error)
}

// accountWindowOfficialUsageReader 只读取已由 OpenAI 上游快照持久化的账号窗口百分比。
// 它不能从 usage_logs、Token 或成员 attributed_percent 反推账号总量。
type accountWindowOfficialUsageReader interface {
	GetAccountOfficialWindowPercent(ctx context.Context, accountID int64, window string) (float64, bool, error)
}

// accountWindowForceUnattributedReader 读取账号是否处于「官方用量强制未归因」模式。
// 可选能力：测试桩可实现它返回 true 以覆盖 force 场景；生产仓储实现。
type accountWindowForceUnattributedReader interface {
	IsAccountWindowForceUnattributed(ctx context.Context, accountID int64) (bool, error)
}

type accountWindowSharedPoolManager interface {
	SetAccountSharedPoolMode(ctx context.Context, accountID int64, enabled bool) error
	ListSharedPoolAccountIDs(ctx context.Context) ([]int64, error)
}

func (s *AccountWindowQuotaService) SetAccountSharedPoolMode(ctx context.Context, accountID int64, enabled bool) error {
	if !s.Enabled() || accountID <= 0 {
		return ErrAccountWindowInvalidMembers
	}
	manager, ok := s.repo.(accountWindowSharedPoolManager)
	if !ok {
		return errors.New("account shared pool mode is unavailable")
	}
	return manager.SetAccountSharedPoolMode(ctx, accountID, enabled)
}

func (s *AccountWindowQuotaService) ListSharedPoolAccountIDs(ctx context.Context) ([]int64, error) {
	if !s.Enabled() {
		return nil, nil
	}
	if manager, ok := s.repo.(accountWindowSharedPoolManager); ok {
		return manager.ListSharedPoolAccountIDs(ctx)
	}
	return nil, nil
}

// AccountWindowUsageLogRef 描述一次已经持久化的 usage_log。
// Inserted 用于区分真正迟到的新日志与幂等重试命中的既有日志。
type AccountWindowUsageLogRef struct {
	ID        int64     `json:"-"`
	CreatedAt time.Time `json:"-"`
	UserID    int64     `json:"-"`
	CostUSD   float64   `json:"-"`
	Inserted  bool      `json:"-"`
	RequestID string    `json:"-"`
	APIKeyID  int64     `json:"-"`
}

// AccountWindowAttributionCheckpoint 是账号窗口增量归因的持久化状态。
// 官方总量只来自上游；pending basis 仅决定后续官方增量如何分配。
type AccountWindowAttributionCheckpoint struct {
	Basis                       string                    `json:"basis"`
	AttributionMode             string                    `json:"attribution_mode"`
	At                          time.Time                 `json:"at"`
	WindowStart                 time.Time                 `json:"window_start"`
	ResetAt                     *time.Time                `json:"reset_at,omitempty"`
	LatestOfficialPercent       float64                   `json:"latest_official_percent"`
	LatestObservedAt            time.Time                 `json:"latest_observed_at"`
	SeenThroughCreatedAt        time.Time                 `json:"seen_through_created_at,omitempty"`
	SeenThroughUsageLogID       int64                     `json:"seen_through_usage_log_id,omitempty"`
	PendingBasisByUser          map[int64]float64         `json:"pending_basis_by_user,omitempty"`
	PendingUnattributedBasisUSD float64                   `json:"pending_unattributed_basis_usd,omitempty"`
	UnattributedPercent         float64                   `json:"unattributed_percent,omitempty"`
	UsageLog                    *AccountWindowUsageLogRef `json:"-"`
	CandidateExpired            bool                      `json:"-"`
}

type accountWindowAttributionCheckpoint = AccountWindowAttributionCheckpoint

// AccountWindowQuotaService 负责把账号级官方利用率增量分摊到发起请求的用户，
// 并提供 23% 配额校验与窗口重置能力。
type AccountWindowQuotaService struct {
	repo     UserAccountWindowQuotaRepository
	rdb      *redis.Client
	configMu sync.Mutex
}

// NewAccountWindowQuotaService 构造 AccountWindowQuotaService。
func NewAccountWindowQuotaService(repo UserAccountWindowQuotaRepository, rdb *redis.Client) *AccountWindowQuotaService {
	return &AccountWindowQuotaService{repo: repo, rdb: rdb}
}

// Enabled 报告服务是否具备分摊/校验所需依赖。
func (s *AccountWindowQuotaService) Enabled() bool {
	return s != nil && s.repo != nil
}

func accountWindowCheckpointKey(accountID int64, window string) string {
	return "uawq:checkpoint:" + strconv.FormatInt(accountID, 10) + ":" + window
}

func AccountWindowAttributionCheckpointExtraKey(window string) string {
	if window == WindowType7d {
		return "window_quota_attribution_checkpoint_7d"
	}
	return "window_quota_attribution_checkpoint_5h"
}

func accountWindowAttributionLockKey(accountID int64, window string) string {
	return "uawq:attribution-lock:" + strconv.FormatInt(accountID, 10) + ":" + window
}

func accountWindowConfigLockKey() string {
	return "uawq:config-lock"
}

// acquireConfigLock 串行化 ceiling、limit、equalize 和成员同步。Redis 锁覆盖多实例，
// 本地互斥锁覆盖无 Redis 的测试/降级环境；Redis 故障时拒绝配置写入，避免静默竞态。
func (s *AccountWindowQuotaService) acquireConfigLock(ctx context.Context) (func(), error) {
	if s == nil {
		return nil, errors.New("account window quota service unavailable")
	}
	s.configMu.Lock()
	if s.rdb == nil {
		return s.configMu.Unlock, nil
	}

	key := accountWindowConfigLockKey()
	token := fmt.Sprintf("%d:%p", time.Now().UnixNano(), s)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		locked, err := s.rdb.SetNX(ctx, key, token, 2*time.Minute).Result()
		if err != nil {
			s.configMu.Unlock()
			return nil, fmt.Errorf("acquire account window configuration lock: %w", err)
		}
		if locked {
			return func() {
				defer s.configMu.Unlock()
				unlockCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				const releaseIfOwner = `if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) else return 0 end`
				if err := s.rdb.Eval(unlockCtx, releaseIfOwner, []string{key}, token).Err(); err != nil {
					slog.Warn("account_window_quota.config_unlock_failed", "error", err)
				}
			}, nil
		}
		select {
		case <-ctx.Done():
			s.configMu.Unlock()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// acquireAttributionLock serializes checkpoint selection, member-share recomputation,
// member resync recomputation, and scheduled reset for one account window across instances.
// Without this lock, an older snapshot can win Redis first, pause in SQL, and overwrite the
// database after a newer snapshot has already committed.
func (s *AccountWindowQuotaService) acquireAttributionLock(ctx context.Context, accountID int64, window string) (func(), error) {
	if s == nil || s.rdb == nil {
		return func() {}, nil
	}
	key := accountWindowAttributionLockKey(accountID, window)
	token := fmt.Sprintf("%d:%p", time.Now().UnixNano(), s)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		locked, err := s.rdb.SetNX(ctx, key, token, 2*time.Minute).Result()
		if err != nil {
			// PostgreSQL 账号行锁和成员行锁才是持久一致性栅栏；Redis 失败时降级为无锁缓存模式。
			slog.Warn("account_window_quota.lock_unavailable_fallback_to_db", "account_id", accountID, "window", window, "error", err)
			return func() {}, nil
		}
		if locked {
			return func() {
				unlockCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				const releaseIfOwner = `if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) else return 0 end`
				if err := s.rdb.Eval(unlockCtx, releaseIfOwner, []string{key}, token).Err(); err != nil {
					slog.Warn("account_window_quota.unlock_failed", "account_id", accountID, "window", window, "error", err)
				}
			}, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func windowLengthSeconds(window string) int {
	if window == WindowType7d {
		return window7dLengthSeconds
	}
	return window5hLengthSeconds
}

func accountWindowCeilingKey(window string) string {
	return "uawq:ceiling:" + window
}

// accountWindowSeatsKey 是车位数（共享人数）配置键，全局一份（本部署 = 一辆车）。
func accountWindowSeatsKey() string {
	return "uawq:seats"
}

// Attribute 保留旧调用接口；没有当前 usage_log 定位信息时走后台快照同步语义。
func (s *AccountWindowQuotaService) Attribute(ctx context.Context, userID, accountID int64, snapshot *OpenAICodexUsageSnapshot) {
	s.AttributeRecordedUsage(ctx, userID, accountID, snapshot, nil)
}

// AttributeRecordedUsage 在 usage_log 已确认持久化后推进增量归因。
func (s *AccountWindowQuotaService) AttributeRecordedUsage(ctx context.Context, userID, accountID int64, snapshot *OpenAICodexUsageSnapshot, usageLog *AccountWindowUsageLogRef) {
	if userID <= 0 {
		return
	}
	if usageLog != nil && usageLog.UserID <= 0 {
		usageLog.UserID = userID
	}
	if err := s.syncOfficialSnapshot(ctx, accountID, snapshot, usageLog); err != nil {
		slog.Warn("account_window_quota.sync_official_snapshot_failed", "account_id", accountID, "error", err)
	}
}

// SyncOfficialSnapshot 同步后台/admin 获取的官方快照；没有请求态 usage_log 上界。
func (s *AccountWindowQuotaService) SyncOfficialSnapshot(ctx context.Context, accountID int64, snapshot *OpenAICodexUsageSnapshot) error {
	return s.syncOfficialSnapshot(ctx, accountID, snapshot, nil)
}

func (s *AccountWindowQuotaService) syncOfficialSnapshot(ctx context.Context, accountID int64, snapshot *OpenAICodexUsageSnapshot, usageLog *AccountWindowUsageLogRef) error {
	if !s.Enabled() || accountID <= 0 || snapshot == nil {
		return nil
	}
	norm := snapshot.Normalize()
	if norm == nil {
		return nil
	}

	processingNow := time.Now()
	observedAt := codexSnapshotBaseTime(snapshot, processingNow)
	var firstErr error
	for _, item := range []struct {
		window       string
		usedPercent  *float64
		resetSeconds *int
	}{
		{window: WindowType5h, usedPercent: norm.Used5hPercent, resetSeconds: norm.Reset5hSeconds},
		{window: WindowType7d, usedPercent: norm.Used7dPercent, resetSeconds: norm.Reset7dSeconds},
	} {
		expired := accountWindowSnapshotExpired(observedAt, processingNow, item.resetSeconds)
		if expired {
			continue
		}
		if err := s.attributeWindowWithUsage(ctx, accountID, item.window, item.usedPercent, item.resetSeconds, observedAt, usageLog, expired); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("sync %s window: %w", item.window, err)
		}
	}
	return firstErr
}

func accountWindowSnapshotExpired(observedAt, processingNow time.Time, resetSeconds *int) bool {
	if resetSeconds == nil || *resetSeconds < 0 || observedAt.IsZero() || processingNow.IsZero() {
		return false
	}
	return !observedAt.Add(time.Duration(*resetSeconds) * time.Second).After(processingNow)
}

func (s *AccountWindowQuotaService) attributeWindow(ctx context.Context, accountID int64, window string, usedPercent *float64, resetSeconds *int, observedAt time.Time) error {
	return s.attributeWindowWithUsage(ctx, accountID, window, usedPercent, resetSeconds, observedAt, nil, false)
}

func (s *AccountWindowQuotaService) attributeWindowWithUsage(ctx context.Context, accountID int64, window string, usedPercent *float64, resetSeconds *int, observedAt time.Time, usageLog *AccountWindowUsageLogRef, candidateExpired bool) error {
	if usedPercent == nil {
		return nil
	}
	officialPercent := normalizeAccountWindowPercent(*usedPercent)
	releaseLock, err := s.acquireAttributionLock(ctx, accountID, window)
	if err != nil {
		return err
	}
	defer releaseLock()

	var resetAt *time.Time
	windowStart := observedAt.Add(-time.Duration(windowLengthSeconds(window)) * time.Second)
	if resetSeconds != nil && *resetSeconds >= 0 {
		t := observedAt.Add(time.Duration(*resetSeconds) * time.Second)
		resetAt = &t
		windowStart = t.Add(-time.Duration(windowLengthSeconds(window)) * time.Second)
	}

	candidate := newAccountWindowCostCheckpoint(officialPercent, windowStart, observedAt)
	if resetAt != nil {
		value := *resetAt
		candidate.ResetAt = &value
	}
	candidate.UsageLog = usageLog
	candidate.CandidateExpired = candidateExpired
	winner, applied, err := s.repo.ApplyWindowSharesFromCheckpoint(ctx, accountID, window, candidate, resetAt)
	if err != nil {
		return fmt.Errorf("apply durable incremental cost attribution: %w", err)
	}
	if !applied {
		return nil
	}
	if err := s.saveAttributionCheckpointCache(ctx, accountID, window, winner); err != nil {
		slog.Warn("account_window_quota.checkpoint_cache_failed", "account_id", accountID, "window", window, "error", err)
	}
	return nil
}

func normalizeAccountWindowPercent(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	return math.Round(value*10000) / 10000
}

// AccountWindowBoundariesMatch reports whether two observed starts belong to the same official window.
func AccountWindowBoundariesMatch(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return false
	}
	return math.Abs(a.Sub(b).Seconds()) <= AccountWindowBoundaryTolerance.Seconds()
}

func sameAccountWindowBoundary(a, b time.Time) bool {
	return AccountWindowBoundariesMatch(a, b)
}

func accountWindowBoundaryIsNewer(candidate, current time.Time) bool {
	if candidate.IsZero() || current.IsZero() {
		return false
	}
	return candidate.After(current.Add(AccountWindowBoundaryTolerance))
}

func validAccountWindowCostCheckpoint(checkpoint *accountWindowAttributionCheckpoint) bool {
	return IsValidAccountWindowAttributionCheckpoint(checkpoint)
}

func IsValidAccountWindowAttributionCheckpoint(checkpoint *AccountWindowAttributionCheckpoint) bool {
	return checkpoint != nil && checkpoint.Basis == accountWindowAttributionBasis &&
		checkpoint.AttributionMode == accountWindowAttributionMode &&
		!checkpoint.At.IsZero() && !checkpoint.WindowStart.IsZero() &&
		!math.IsNaN(checkpoint.LatestOfficialPercent) && !math.IsInf(checkpoint.LatestOfficialPercent, 0) &&
		checkpoint.LatestOfficialPercent >= 0 &&
		!math.IsNaN(checkpoint.UnattributedPercent) && !math.IsInf(checkpoint.UnattributedPercent, 0) &&
		checkpoint.UnattributedPercent >= 0
}

func CloneAccountWindowAttributionCheckpoint(checkpoint AccountWindowAttributionCheckpoint) AccountWindowAttributionCheckpoint {
	cloned := checkpoint
	if checkpoint.ResetAt != nil {
		value := *checkpoint.ResetAt
		cloned.ResetAt = &value
	}
	if checkpoint.PendingBasisByUser == nil {
		cloned.PendingBasisByUser = make(map[int64]float64)
	} else {
		cloned.PendingBasisByUser = make(map[int64]float64, len(checkpoint.PendingBasisByUser))
		for userID, cost := range checkpoint.PendingBasisByUser {
			cloned.PendingBasisByUser[userID] = cost
		}
	}
	if checkpoint.UsageLog != nil {
		value := *checkpoint.UsageLog
		cloned.UsageLog = &value
	}
	return cloned
}

// SelectAccountWindowAttributionCheckpoint chooses the durable winner for one
// account window. Same-window official totals are monotonic; stale observations
// and stale boundaries are rejected without mutating pending attribution state.
func SelectAccountWindowAttributionCheckpoint(existing *AccountWindowAttributionCheckpoint, candidate AccountWindowAttributionCheckpoint, candidateBoundaryKnown bool) (AccountWindowAttributionCheckpoint, bool) {
	if !IsValidAccountWindowAttributionCheckpoint(existing) {
		return CloneAccountWindowAttributionCheckpoint(candidate), true
	}
	current := CloneAccountWindowAttributionCheckpoint(*existing)
	if candidateBoundaryKnown && accountWindowBoundaryIsNewer(current.WindowStart, candidate.WindowStart) {
		return current, false
	}
	if !candidateBoundaryKnown || sameAccountWindowBoundary(current.WindowStart, candidate.WindowStart) {
		if !current.LatestObservedAt.IsZero() && candidate.LatestObservedAt.Before(current.LatestObservedAt) {
			return current, false
		}
		winner := current
		if candidate.LatestOfficialPercent > current.LatestOfficialPercent+windowQuotaEpsilon {
			winner.LatestOfficialPercent = normalizeAccountWindowPercent(candidate.LatestOfficialPercent)
		}
		if candidate.LatestObservedAt.After(current.LatestObservedAt) {
			winner.LatestObservedAt = candidate.LatestObservedAt
		}
		// Keep the first durable boundary for the current window. Upstream reset-after values can
		// drift by several minutes between responses; replacing the canonical boundary each time
		// would let that jitter accumulate and eventually look like a rollover.
		if winner.ResetAt == nil && candidate.ResetAt != nil {
			value := *candidate.ResetAt
			winner.ResetAt = &value
		}
		return winner, true
	}
	return CloneAccountWindowAttributionCheckpoint(candidate), true
}

func NewAccountWindowAttributionCheckpoint(officialPercent float64, windowStart, observedAt time.Time, resetAt *time.Time) AccountWindowAttributionCheckpoint {
	checkpoint := newAccountWindowCostCheckpoint(officialPercent, windowStart, observedAt)
	if resetAt != nil {
		value := *resetAt
		checkpoint.ResetAt = &value
	}
	return checkpoint
}

func newAccountWindowCostCheckpoint(officialPercent float64, windowStart, observedAt time.Time) accountWindowAttributionCheckpoint {
	return accountWindowAttributionCheckpoint{
		Basis:                 accountWindowAttributionBasis,
		AttributionMode:       accountWindowAttributionMode,
		At:                    windowStart,
		WindowStart:           windowStart,
		LatestOfficialPercent: normalizeAccountWindowPercent(officialPercent),
		LatestObservedAt:      observedAt,
		PendingBasisByUser:    make(map[int64]float64),
	}
}

func (s *AccountWindowQuotaService) saveAttributionCheckpointCache(ctx context.Context, accountID int64, window string, checkpoint AccountWindowAttributionCheckpoint) error {
	if s == nil || s.rdb == nil {
		return nil
	}
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, accountWindowCheckpointKey(accountID, window), encoded, time.Duration(accountWindowCheckpointTTLSeconds)*time.Second).Err()
}

// windowQuotaEpsilon 是浮点比较容差，避免 decimal→float 误差在边界处导致放行/拦截抖动。
const windowQuotaEpsilon = 1e-9

// accountWindowPoolView 汇总某账号全员 5h/7d 配额快照，用于计算两个窗口各自「救急池」的
// 可借总额与借用者的均分增量。
//
// 救急池模型（5h、7d 各自独立，机制完全一致；全程自愿，绝不强制收他人份额）：
//   - 捐赠者（donate_fraction>0）自愿让出本窗口未用份额，自留有效上限 = max(已用, 基础×(1-比例))；
//   - 借用者（非捐赠者、已达自己基础上限）按人数均分该窗口救急池；
//   - 不捐的人份额谁也动不了；无人捐 → 退化为原硬性上限（7d 即铁底线）。
//   - 守恒：Σ 有效上限 = Σ 基础上限 ≤ ceiling，绝不把账号推过官方安全水位。
type accountWindowPoolView struct {
	base5h  map[int64]float64
	used5h  map[int64]float64
	frac5h  map[int64]float64
	reset5h map[int64]*time.Time
	limit7d map[int64]float64
	used7d  map[int64]float64
	frac7d  map[int64]float64
	reset7d map[int64]*time.Time
}

func clampFraction(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

func donatedCapacity(base, used, fraction float64) float64 {
	if base <= 0 {
		return 0
	}
	keep := math.Max(used, base*(1-clampFraction(fraction)))
	return math.Max(0, base-keep)
}

// MinimumAccountWindowDonateFraction 返回用户当前能降到的最低捐赠比例。
// 已被其他成员实际用到基础上限之外的部分视为“已借出”，在窗口重置前必须由现有捐赠覆盖。
func MinimumAccountWindowDonateFraction(records []UserAccountWindowQuotaRecord, userID int64, window string) float64 {
	return buildAccountWindowPoolView(records).minimumDonateFraction(userID, window)
}

// ValidateAccountWindowDonateFraction 拒绝收回已经被其他成员借用的额度。
func ValidateAccountWindowDonateFraction(records []UserAccountWindowQuotaRecord, userID int64, window string, fraction float64) error {
	minimum := MinimumAccountWindowDonateFraction(records, userID, window)
	if clampFraction(fraction)+windowQuotaEpsilon < minimum {
		return &AccountWindowDonationInUseError{MinimumFraction: minimum}
	}
	return nil
}

func buildAccountWindowPoolView(records []UserAccountWindowQuotaRecord) *accountWindowPoolView {
	pv := &accountWindowPoolView{
		base5h:  map[int64]float64{},
		used5h:  map[int64]float64{},
		frac5h:  map[int64]float64{},
		reset5h: map[int64]*time.Time{},
		limit7d: map[int64]float64{},
		used7d:  map[int64]float64{},
		frac7d:  map[int64]float64{},
		reset7d: map[int64]*time.Time{},
	}
	for _, r := range records {
		switch r.WindowType {
		case WindowType5h:
			pv.base5h[r.UserID] = r.LimitPercent
			pv.used5h[r.UserID] = r.AttributedPercent
			pv.frac5h[r.UserID] = clampFraction(r.DonatePoolFraction)
			pv.reset5h[r.UserID] = r.WindowResetAt
		case WindowType7d:
			pv.limit7d[r.UserID] = r.LimitPercent
			pv.used7d[r.UserID] = r.AttributedPercent
			pv.frac7d[r.UserID] = clampFraction(r.DonatePoolFraction)
			pv.reset7d[r.UserID] = r.WindowResetAt
		}
	}
	return pv
}

func (pv *accountWindowPoolView) minimumDonateFraction(userID int64, window string) float64 {
	limits, used, fractions := pv.base5h, pv.used5h, pv.frac5h
	if window == WindowType7d {
		limits, used, fractions = pv.limit7d, pv.used7d, pv.frac7d
	}
	base, ok := limits[userID]
	if !ok || base <= 0 {
		return 0
	}
	borrowed := 0.0
	otherCapacity := 0.0
	for memberID, memberBase := range limits {
		borrowed += math.Max(0, used[memberID]-memberBase)
		if memberID != userID {
			otherCapacity += donatedCapacity(memberBase, used[memberID], fractions[memberID])
		}
	}
	minimum := clampFraction(math.Max(0, borrowed-otherCapacity) / base)
	current := clampFraction(fractions[userID])
	if minimum > current {
		return current
	}
	return minimum
}

// within7d 报告用户 7d 周配额是否仍有余量（无 7d 记录或上限<=0 视为不限）。
// 按 7d 有效上限判断：正当借用 7d 救急池的用户不应被排除在 5h 借用之外，
// 与 CheckUserAccountEligible 第 1 步的 7d 判定口径保持一致。
func (pv *accountWindowPoolView) within7d(userID int64) bool {
	limit, ok := pv.limit7d[userID]
	if !ok || limit <= 0 {
		return true
	}
	return pv.used7d[userID]+windowQuotaEpsilon < pv.effective7dLimit(userID)
}

// donor5hKeepCap 返回捐赠者 5h 的自留有效上限 = max(已用, 基础上限×(1-捐赠比例))。
// 捐赠者最多用到这条线，其余让给救急池；已用超过自留线则被冻结在已用值（不能再用，需调低捐赠比例）。
func (pv *accountWindowPoolView) donor5hKeepCap(userID int64) float64 {
	base := pv.base5h[userID]
	keep := base * (1 - pv.frac5h[userID])
	if used := pv.used5h[userID]; used > keep {
		return used
	}
	return keep
}

// pool5hCapacity 返回 5h 救急池捐赠总容量。
func (pv *accountWindowPoolView) pool5hCapacity() float64 {
	pool := 0.0
	for userID, frac := range pv.frac5h {
		if frac <= 0 {
			continue
		}
		pool += donatedCapacity(pv.base5h[userID], pv.used5h[userID], frac)
	}
	return pool
}

func (pv *accountWindowPoolView) borrowed5hUsed() float64 {
	borrowed := 0.0
	for userID, used := range pv.used5h {
		borrowed += math.Max(0, used-pv.base5h[userID])
	}
	return borrowed
}

// pool5hAvailable 返回尚未被实际借走的 5h 救急池余额。
func (pv *accountWindowPoolView) pool5hAvailable() float64 {
	return math.Max(0, pv.pool5hCapacity()-pv.borrowed5hUsed())
}

// needy5hCount 返回当前正缺额者人数（非捐赠者、已达自己 5h 基础上限、且 7d 未打满）。
func (pv *accountWindowPoolView) needy5hCount() int {
	n := 0
	for userID, base := range pv.base5h {
		if pv.frac5h[userID] > 0 || base <= 0 {
			continue
		}
		if pv.used5h[userID]+windowQuotaEpsilon < base {
			continue
		}
		if !pv.within7d(userID) {
			continue
		}
		n++
	}
	return n
}

// effective5hLimit 返回用户 5h 的有效上限（判定与展示共用）：
//   - 捐赠者：自留有效上限；
//   - 借用者（非捐赠者、已达基础上限、7d 未满）：基础上限 + 救急池均分增量；
//   - 其他非捐赠者：基础上限。
func (pv *accountWindowPoolView) effective5hLimit(userID int64) float64 {
	base := pv.base5h[userID]
	if pv.frac5h[userID] > 0 {
		return pv.donor5hKeepCap(userID)
	}
	if base <= 0 {
		return base
	}
	if pv.used5h[userID]+windowQuotaEpsilon < base {
		return base
	}
	if !pv.within7d(userID) {
		return base
	}
	needy := pv.needy5hCount()
	capacity := pv.pool5hCapacity()
	if needy <= 0 || capacity <= 0 {
		return base
	}
	// 借用额度同时受公平份额（capacity/needy）与池内实际余额约束：
	// 别人已经借走的容量不能再许诺给后来者，否则 Σ实际用量会超出捐赠总量。
	ownBorrowed := math.Max(0, pv.used5h[userID]-base)
	allowance := math.Min(capacity/float64(needy), pv.pool5hAvailable()+ownBorrowed)
	if allowance <= 0 {
		return base
	}
	return base + allowance
}

// memberCount 返回某窗口下账号当前活跃成员数。
func (pv *accountWindowPoolView) memberCount(window string) int {
	if window == WindowType7d {
		return len(pv.limit7d)
	}
	return len(pv.base5h)
}

// sumConfigured 返回某窗口下账号全员配置上限之和。
func (pv *accountWindowPoolView) clearExpiredUsage(now time.Time) {
	if pv == nil || now.IsZero() {
		return
	}
	for userID, resetAt := range pv.reset5h {
		if resetAt != nil && !resetAt.After(now) {
			pv.used5h[userID] = 0
			pv.frac5h[userID] = 0
			delete(pv.reset5h, userID)
		}
	}
	for userID, resetAt := range pv.reset7d {
		if resetAt != nil && !resetAt.After(now) {
			pv.used7d[userID] = 0
			delete(pv.reset7d, userID)
		}
	}
}

func (pv *accountWindowPoolView) sumConfigured(window string) float64 {
	m := pv.base5h
	if window == WindowType7d {
		m = pv.limit7d
	}
	sum := 0.0
	for _, v := range m {
		sum += v
	}
	return sum
}

func (pv *accountWindowPoolView) sumAttributed(window string) float64 {
	m := pv.used5h
	if window == WindowType7d {
		m = pv.used7d
	}
	sum := 0.0
	for _, value := range m {
		if value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0) {
			sum += value
		}
	}
	return normalizeAccountWindowPercent(sum)
}

// resolveAccountWindowOfficialPercent 只从 PostgreSQL 中持久化的 OpenAI 官方快照链读取账号总量。
// Redis checkpoint 只是提交后的兼容缓存，不能参与 eligibility 或展示等正确性路径。
func (s *AccountWindowQuotaService) resolveAccountWindowOfficialPercent(ctx context.Context, accountID int64, window string) (float64, bool) {
	if s == nil || s.repo == nil {
		return 0, false
	}
	reader, ok := s.repo.(accountWindowOfficialUsageReader)
	if !ok {
		return 0, false
	}
	value, found, err := reader.GetAccountOfficialWindowPercent(ctx, accountID, window)
	if err != nil {
		slog.Warn("account_window_quota.official_extra_read_failed", "account_id", accountID, "window", window, "error", err)
		return 0, false
	}
	if !found {
		return 0, false
	}
	return normalizeAccountWindowPercent(value), true
}

// donor7dKeepCap 返回 7d 捐赠者的自留有效上限 = max(已用, 基础上限×(1-捐赠比例))。
// 与 5h 完全一致：捐赠者最多用到这条线，其余自愿让给 7d 救急池；已用超过自留线则冻结在已用值。
func (pv *accountWindowPoolView) donor7dKeepCap(userID int64) float64 {
	base := pv.limit7d[userID]
	keep := base * (1 - pv.frac7d[userID])
	if used := pv.used7d[userID]; used > keep {
		return used
	}
	return keep
}

// pool7dCapacity 返回 7d 救急池捐赠总容量。
func (pv *accountWindowPoolView) pool7dCapacity() float64 {
	pool := 0.0
	for userID, frac := range pv.frac7d {
		if frac <= 0 {
			continue
		}
		pool += donatedCapacity(pv.limit7d[userID], pv.used7d[userID], frac)
	}
	return pool
}

func (pv *accountWindowPoolView) borrowed7dUsed() float64 {
	borrowed := 0.0
	for userID, used := range pv.used7d {
		borrowed += math.Max(0, used-pv.limit7d[userID])
	}
	return borrowed
}

// pool7dAvailable 返回尚未被实际借走的 7d 救急池余额。
func (pv *accountWindowPoolView) pool7dAvailable() float64 {
	return math.Max(0, pv.pool7dCapacity()-pv.borrowed7dUsed())
}

// needy7dCount 返回当前正缺额者人数（非捐赠者、已达自己 7d 基础上限）。
func (pv *accountWindowPoolView) needy7dCount() int {
	n := 0
	for userID, base := range pv.limit7d {
		if pv.frac7d[userID] > 0 || base <= 0 {
			continue
		}
		if pv.used7d[userID]+windowQuotaEpsilon >= base {
			n++
		}
	}
	return n
}

// effective7dLimit 返回用户 7d 的有效上限（与 5h 同构，全程自愿）：
//   - 捐赠者：自留有效上限；
//   - 借用者（非捐赠者、已达基础上限）：基础上限 + 7d 救急池均分增量；
//   - 其他人 / 无人捐赠：基础上限（即铁底线，谁也动不了你没捐的份额）。
//
// 由账号级硬闸（CheckUserAccountEligible 第 0 步）兜底，保证 Σ实际用量 ≤ ceiling。
func (pv *accountWindowPoolView) effective7dLimit(userID int64) float64 {
	base := pv.limit7d[userID]
	if pv.frac7d[userID] > 0 {
		return pv.donor7dKeepCap(userID)
	}
	if base <= 0 {
		return base
	}
	if pv.used7d[userID]+windowQuotaEpsilon < base {
		return base
	}
	needy := pv.needy7dCount()
	capacity := pv.pool7dCapacity()
	if needy <= 0 || capacity <= 0 {
		return base
	}
	// 与 5h 同构：借用额度受公平份额与池内实际余额双重约束，防止超借。
	ownBorrowed := math.Max(0, pv.used7d[userID]-base)
	allowance := math.Min(capacity/float64(needy), pv.pool7dAvailable()+ownBorrowed)
	if allowance <= 0 {
		return base
	}
	return base + allowance
}

func effectiveWindowQuotaRecordAt(record UserAccountWindowQuotaRecord, now time.Time) UserAccountWindowQuotaRecord {
	if record.WindowResetAt == nil || record.WindowResetAt.After(now) {
		return record
	}
	record.AttributedPercent = 0
	record.WindowResetAt = nil
	if record.WindowType == WindowType5h {
		record.DonatePoolFraction = 0
	}
	return record
}

func effectiveWindowQuotaRecordsAt(records []UserAccountWindowQuotaRecord, now time.Time) []UserAccountWindowQuotaRecord {
	effective := make([]UserAccountWindowQuotaRecord, len(records))
	for i, record := range records {
		effective[i] = effectiveWindowQuotaRecordAt(record, now)
	}
	return effective
}

// CheckUserAccountEligible 校验用户在某账号上是否仍可发起请求。
// 规则：5h、7d 都在自己份额之上可借各自的「自愿救急池」（只用别人主动捐出的部分；无人捐则纯硬上限）。
// 返回 eligible=false 时附带被打满的窗口与其重置时间，供调用方生成 429。
// fail-open：底层错误一律放行（不因配额组件故障阻断业务）。
func (s *AccountWindowQuotaService) CheckUserAccountEligible(ctx context.Context, userID, accountID int64) (eligible bool, blockedWindow string, resetAt *time.Time) {
	if !s.Enabled() || userID <= 0 || accountID <= 0 {
		return true, "", nil
	}
	records, err := s.repo.ListByAccount(ctx, accountID)
	if err != nil {
		slog.Warn("account_window_quota.eligibility_read_failed", "user_id", userID, "account_id", accountID, "error", err)
		return true, "", nil
	}
	pv := buildAccountWindowPoolView(records)
	pv.clearExpiredUsage(time.Now())
	ceiling7d := s.GetTotalCeiling(ctx, WindowType7d)
	ceiling5h := s.GetTotalCeiling(ctx, WindowType5h)

	// 拼车账号已有显式成员时，未分配用户不得使用该账号，也不得靠后续归因自动混入成员列表。
	_, has5hMembership := pv.base5h[userID]
	_, has7dMembership := pv.limit7d[userID]
	if len(records) == 0 || (len(pv.base5h) > 0 && !has5hMembership) || (len(pv.limit7d) > 0 && !has7dMembership) {
		return false, WindowType5h, nil
	}

	// 0) 账号级硬闸只读取 OpenAI 官方账号窗口百分比。成员归因之和、
	// usage_logs.total_cost 和 Token 都不能充当账号总量来源；官方快照暂缺时 fail-open。
	if official7d, found := s.resolveAccountWindowOfficialPercent(ctx, accountID, WindowType7d); found && official7d+windowQuotaEpsilon >= ceiling7d {
		return false, WindowType7d, pv.reset7d[userID]
	}
	if official5h, found := s.resolveAccountWindowOfficialPercent(ctx, accountID, WindowType5h); found && official5h+windowQuotaEpsilon >= ceiling5h {
		return false, WindowType5h, pv.reset5h[userID]
	}
	// The account flag is read with the member rows, so toggles need no cache
	// invalidation. Membership and both official ceilings remain mandatory.
	if records[0].SharedPoolMode {
		return true, "", nil
	}

	// 1) 7d：自己份额 + 自愿 7d 救急池（捐赠者受自留上限约束）。
	//    无人捐赠时 effective7dLimit == 基础上限，即纯铁底线，谁也动不了你没捐的份额。
	if limit, ok := pv.limit7d[userID]; ok && limit > 0 {
		if pv.used7d[userID]+windowQuotaEpsilon >= pv.effective7dLimit(userID) {
			return false, WindowType7d, pv.reset7d[userID]
		}
	}

	// 2) 5h：自己份额 + 救急池增量（捐赠者受自留上限约束）。
	base, has5h := pv.base5h[userID]
	if !has5h || base <= 0 {
		return true, "", nil
	}
	if pv.used5h[userID]+windowQuotaEpsilon < pv.effective5hLimit(userID) {
		return true, "", nil
	}
	return false, WindowType5h, pv.reset5h[userID]
}

// ResetDueWindows 扫描已到期窗口并清零（定时兜底，应对官方未回落或回落未被观测到的情况）。
func (s *AccountWindowQuotaService) ResetDueWindows(ctx context.Context) {
	if !s.Enabled() {
		return
	}
	dueAt := time.Now()
	due, err := s.repo.ListDueResets(ctx, dueAt)
	if err != nil {
		slog.Warn("account_window_quota.list_due_resets_failed", "error", err)
		return
	}
	for _, d := range due {
		releaseLock, lockErr := s.acquireAttributionLock(ctx, d.AccountID, d.WindowType)
		if lockErr != nil {
			slog.Warn("account_window_quota.timed_reset_lock_failed", "account_id", d.AccountID, "window", d.WindowType, "error", lockErr)
			continue
		}
		reset, err := s.repo.ResetWindowForAccountIfDue(ctx, d.AccountID, d.WindowType, dueAt)
		if err != nil {
			releaseLock()
			slog.Warn("account_window_quota.timed_reset_failed", "account_id", d.AccountID, "window", d.WindowType, "error", err)
			continue
		}
		if !reset {
			releaseLock()
			continue
		}
		if s.rdb != nil {
			if err := s.rdb.Del(ctx, accountWindowCheckpointKey(d.AccountID, d.WindowType)).Err(); err != nil {
				slog.Warn("account_window_quota.checkpoint_reset_failed", "account_id", d.AccountID, "window", d.WindowType, "error", err)
			}
		}
		releaseLock()
	}
}

// ListUserWindows 返回用户的全部窗口配额（供前端展示）。
func (s *AccountWindowQuotaService) ListUserWindows(ctx context.Context, userID int64) ([]UserAccountWindowQuotaRecord, error) {
	if s == nil || s.repo == nil {
		return nil, nil
	}
	records, err := s.repo.ListByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return effectiveWindowQuotaRecordsAt(records, time.Now()), nil
}

// ListSharedAccountIDs 返回所有启用拼车额度的账号 ID，包括尚未创建成员额度行的账号。
func (s *AccountWindowQuotaService) ListSharedAccountIDs(ctx context.Context) ([]int64, error) {
	if s == nil || s.repo == nil {
		return nil, nil
	}
	reader, ok := s.repo.(sharedAccountIDReader)
	if !ok {
		return nil, nil
	}
	return reader.ListSharedAccountIDs(ctx)
}

// ListAllForAdmin 返回所有用户在所有账号所有窗口的配额（带用户信息，管理端总览用）。
// 顺带按账号建救急池视图，补上"有效上限/池剩余"，使管理端与用户端口径一致（能看出谁在借池）。
func (s *AccountWindowQuotaService) ListAllForAdmin(ctx context.Context) ([]AdminWindowQuotaOverviewRow, []AdminWindowQuotaSummary, error) {
	if s == nil || s.repo == nil {
		return nil, nil, nil
	}
	rows, err := s.repo.ListAllWithUser(ctx)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	for i := range rows {
		effective := effectiveWindowQuotaRecordAt(UserAccountWindowQuotaRecord{
			UserID:             rows[i].UserID,
			AccountID:          rows[i].AccountID,
			WindowType:         rows[i].WindowType,
			LimitPercent:       rows[i].LimitPercent,
			AttributedPercent:  rows[i].AttributedPercent,
			WindowResetAt:      rows[i].WindowResetAt,
			DonatePoolFraction: rows[i].DonatePoolFraction,
		}, now)
		rows[i].AttributedPercent = effective.AttributedPercent
		rows[i].WindowResetAt = effective.WindowResetAt
		rows[i].DonatePoolFraction = effective.DonatePoolFraction
	}
	// 按账号汇总成池视图。
	byAccount := map[int64][]UserAccountWindowQuotaRecord{}
	for _, r := range rows {
		byAccount[r.AccountID] = append(byAccount[r.AccountID], UserAccountWindowQuotaRecord{
			UserID:             r.UserID,
			AccountID:          r.AccountID,
			WindowType:         r.WindowType,
			LimitPercent:       r.LimitPercent,
			AttributedPercent:  r.AttributedPercent,
			WindowResetAt:      r.WindowResetAt,
			DonatePoolFraction: r.DonatePoolFraction,
		})
	}
	pvByAccount := make(map[int64]*accountWindowPoolView, len(byAccount))
	for acc, recs := range byAccount {
		pvByAccount[acc] = buildAccountWindowPoolView(recs)
	}
	ceilings := map[string]float64{
		WindowType5h: s.GetTotalCeiling(ctx, WindowType5h),
		WindowType7d: s.GetTotalCeiling(ctx, WindowType7d),
	}
	for i := range rows {
		pv := pvByAccount[rows[i].AccountID]
		if pv == nil {
			rows[i].EffectiveLimitPercent = rows[i].LimitPercent
			continue
		}
		switch rows[i].WindowType {
		case WindowType5h:
			rows[i].EffectiveLimitPercent = pv.effective5hLimit(rows[i].UserID)
			rows[i].PoolAvailablePercent = pv.pool5hAvailable()
		case WindowType7d:
			rows[i].EffectiveLimitPercent = pv.effective7dLimit(rows[i].UserID)
			rows[i].PoolAvailablePercent = pv.pool7dAvailable()
		default:
			rows[i].EffectiveLimitPercent = rows[i].LimitPercent
		}
	}

	summaries := make([]AdminWindowQuotaSummary, 0, len(byAccount)*len(accountWindowTypes))
	seenSummary := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		key := strconv.FormatInt(row.AccountID, 10) + ":" + row.WindowType
		if _, ok := seenSummary[key]; ok {
			continue
		}
		seenSummary[key] = struct{}{}
		pv := pvByAccount[row.AccountID]
		if pv == nil {
			continue
		}
		configured := pv.sumConfigured(row.WindowType)
		attributed := pv.sumAttributed(row.WindowType)
		ceiling := ceilings[row.WindowType]
		var officialUsedPtr *float64
		var unattributedPtr *float64
		if officialUsed, found := s.resolveAccountWindowOfficialPercent(ctx, row.AccountID, row.WindowType); found {
			value := officialUsed
			officialUsedPtr = &value
			unattributed := normalizeAccountWindowPercent(math.Max(0, officialUsed-attributed))
			unattributedPtr = &unattributed
		}
		summaries = append(summaries, AdminWindowQuotaSummary{
			SharedPoolMode:       row.SharedPoolMode,
			AccountID:            row.AccountID,
			WindowType:           row.WindowType,
			MemberCount:          pv.memberCount(row.WindowType),
			ConfiguredSumPercent: configured,
			AttributedSumPercent: attributed,
			OfficialUsedPercent:  officialUsedPtr,
			UnattributedPercent:  unattributedPtr,
			CeilingPercent:       ceiling,
			Overallocated:        configured > ceiling+windowQuotaEpsilon,
		})
	}
	return rows, summaries, nil
}

// ListSharedForUser 返回当前用户实际参与的拼车账号内全体成员额度。
// 先按当前用户的额度行确定可见账号集合，再过滤全量管理视图，避免普通用户看到其他拼车组。
func (s *AccountWindowQuotaService) ListSharedForUser(ctx context.Context, userID int64) ([]AdminWindowQuotaOverviewRow, []AdminWindowQuotaSummary, error) {
	if userID <= 0 {
		return nil, nil, fmt.Errorf("user_id is required")
	}
	rows, summaries, err := s.ListAllForAdmin(ctx)
	if err != nil {
		return nil, nil, err
	}
	visibleAccounts := make(map[int64]struct{})
	for _, row := range rows {
		if row.UserID == userID {
			visibleAccounts[row.AccountID] = struct{}{}
		}
	}
	sharedRows := make([]AdminWindowQuotaOverviewRow, 0, len(rows))
	for _, row := range rows {
		if _, ok := visibleAccounts[row.AccountID]; ok {
			sharedRows = append(sharedRows, row)
		}
	}
	sharedSummaries := make([]AdminWindowQuotaSummary, 0, len(summaries))
	for _, summary := range summaries {
		if _, ok := visibleAccounts[summary.AccountID]; ok {
			sharedSummaries = append(sharedSummaries, summary)
		}
	}
	return sharedRows, sharedSummaries, nil
}

// IsValidWindowType 报告 window 是否为受支持的官方窗口类型。
func IsValidWindowType(window string) bool {
	return window == WindowType5h || window == WindowType7d
}

// GetTotalCeiling 返回某窗口的总额上限（所有用户 limit 之和的上限）。
// 读 Redis 覆盖值，缺失/非法时回落到 DefaultAccountWindowTotalCeilingPercent。
func (s *AccountWindowQuotaService) GetTotalCeiling(ctx context.Context, window string) float64 {
	if s == nil || s.rdb == nil || !IsValidWindowType(window) {
		return DefaultAccountWindowTotalCeilingPercent
	}
	v, err := s.rdb.Get(ctx, accountWindowCeilingKey(window)).Result()
	if err != nil {
		return DefaultAccountWindowTotalCeilingPercent
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 || f > 100 {
		return DefaultAccountWindowTotalCeilingPercent
	}
	return f
}

// SetTotalCeiling 设置某窗口的总额上限（运行时可配，存 Redis，无 TTL）。
// 写入前校验所有账号现有 configured sum，禁止把 ceiling 降到任何现有配置之下而制造新的超配不一致。
func (s *AccountWindowQuotaService) SetTotalCeiling(ctx context.Context, window string, percent float64) error {
	if s == nil || s.rdb == nil || s.repo == nil {
		return errors.New("account window quota service unavailable")
	}
	if !IsValidWindowType(window) {
		return fmt.Errorf("invalid window type: %s", window)
	}
	if percent <= 0 || percent > 100 {
		return fmt.Errorf("ceiling percent must be in (0,100], got %.2f", percent)
	}
	release, err := s.acquireConfigLock(ctx)
	if err != nil {
		return err
	}
	defer release()
	maxConfigured, err := s.repo.GetMaxConfiguredSumForWindow(ctx, window)
	if err != nil {
		return fmt.Errorf("check existing configured sums before setting ceiling: %w", err)
	}
	if maxConfigured > percent+windowQuotaEpsilon {
		return fmt.Errorf("%w: window=%s requested %.2f%%, highest configured sum is %.2f%%; lower account limits or equalize members first",
			ErrAccountWindowCeilingBelowConfigured, window, percent, maxConfigured)
	}
	return s.rdb.Set(ctx, accountWindowCeilingKey(window), strconv.FormatFloat(percent, 'f', -1, 64), 0).Err()
}

// GetSeats 返回车位数（共享同一账号的人数），缺失/非法回落到 DefaultAccountSeats。
func (s *AccountWindowQuotaService) GetSeats(ctx context.Context) int {
	if s == nil || s.rdb == nil {
		return DefaultAccountSeats
	}
	v, err := s.rdb.Get(ctx, accountWindowSeatsKey()).Result()
	if err != nil {
		return DefaultAccountSeats
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 || n > 100 {
		return DefaultAccountSeats
	}
	return n
}

// SetSeats 设置车位数（运行时可配，存 Redis，无 TTL）。换 3/5/8 人车只改这个。
func (s *AccountWindowQuotaService) SetSeats(ctx context.Context, seats int) error {
	if s == nil || s.rdb == nil {
		return errors.New("account window quota service unavailable")
	}
	if seats <= 0 || seats > 100 {
		return fmt.Errorf("seats must be in [1,100], got %d", seats)
	}
	return s.rdb.Set(ctx, accountWindowSeatsKey(), strconv.Itoa(seats), 0).Err()
}

// defaultUserLimitPercent 返回指定窗口的人均默认上限 = ceiling/seats。
// 仅用于自动建行；管理端显式设置的 limit 不受影响。
func (s *AccountWindowQuotaService) defaultUserLimitPercent(ctx context.Context, window string) float64 {
	if s == nil || s.rdb == nil || !IsValidWindowType(window) {
		return DefaultAccountWindowLimitPercent
	}
	seats := s.GetSeats(ctx)
	if seats <= 0 {
		return DefaultAccountWindowLimitPercent
	}
	dl := s.GetTotalCeiling(ctx, window) / float64(seats)
	if dl <= 0 || dl > 100 {
		return DefaultAccountWindowLimitPercent
	}
	return dl
}

// SetLimitForUserAccount 设置某 (user, account, window) 的 limit_percent（管理端覆盖默认 23%）。
// 护栏：同账号同窗口「其余活跃用户 limit 之和 + 本次 limit」不得超过该窗口总额上限，
// 否则返回 ErrAccountWindowCeilingExceeded（防止 4 人之和占满官方窗口触发风控）。
func (s *AccountWindowQuotaService) SetLimitForUserAccount(ctx context.Context, userID, accountID int64, window string, limitPercent float64) error {
	if s == nil || s.repo == nil {
		return nil
	}
	if !IsValidWindowType(window) {
		return fmt.Errorf("invalid window type: %s", window)
	}
	if limitPercent < 0 {
		limitPercent = 0
	}
	release, err := s.acquireConfigLock(ctx)
	if err != nil {
		return err
	}
	defer release()
	ceiling := s.GetTotalCeiling(ctx, window)
	if setter, ok := s.repo.(accountWindowAtomicLimitSetter); ok {
		return setter.SetLimitForUserAccountWithinCeiling(ctx, userID, accountID, window, limitPercent, ceiling)
	}
	records, err := s.repo.ListByAccount(ctx, accountID)
	if err != nil {
		return fmt.Errorf("check account window configured sum before setting limit: %w", err)
	}
	sumOthers := 0.0
	currentLimit := 0.0
	for _, r := range records {
		if r.WindowType != window {
			continue
		}
		if r.UserID == userID {
			currentLimit = r.LimitPercent
			continue
		}
		sumOthers += r.LimitPercent
	}
	prospectiveSum := sumOthers + limitPercent
	// 已经超配时仍允许持平或降额，便于管理员逐步修复；只有实际增额才严格拒绝。
	if prospectiveSum > ceiling+windowQuotaEpsilon && limitPercent > currentLimit+windowQuotaEpsilon {
		return fmt.Errorf("%w: account=%d window=%s configured sum would be %.2f%% (current user %.2f%% -> %.2f%%), ceiling %.2f%%",
			ErrAccountWindowCeilingExceeded, accountID, window, prospectiveSum, currentLimit, limitPercent, ceiling)
	}
	return s.repo.SetLimitForUserAccount(ctx, userID, accountID, window, limitPercent)
}

// EqualizeAccountActiveMemberLimits 把指定账号 5h/7d 的 ceiling 分别均分给各窗口当前活跃成员。
// 成员发现、份额计算和两窗口更新全部由 repository 在同一事务内完成；任一窗口无成员或任一更新失败都整体回滚。
func (s *AccountWindowQuotaService) EqualizeAccountActiveMemberLimits(ctx context.Context, accountID int64) ([]AccountWindowEqualizationResult, error) {
	if s == nil || s.repo == nil || s.rdb == nil {
		return nil, errors.New("account window quota service unavailable")
	}
	if accountID <= 0 {
		return nil, fmt.Errorf("account_id is required")
	}
	release, err := s.acquireConfigLock(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	results, err := s.repo.EqualizeActiveMemberLimits(
		ctx,
		accountID,
		s.GetTotalCeiling(ctx, WindowType5h),
		s.GetTotalCeiling(ctx, WindowType7d),
	)
	if err != nil {
		return nil, err
	}
	return results, nil
}

// BootstrapSharedAccountMembers 为新开启的拼车账号自动创建成员额度行。
// 若账号已有成员则保持不变；否则继承同分组既有拼车账号的活跃成员。
func (s *AccountWindowQuotaService) BootstrapSharedAccountMembers(ctx context.Context, accountID int64) ([]AccountWindowEqualizationResult, error) {
	if s == nil || s.repo == nil || s.rdb == nil {
		return nil, errors.New("account window quota service unavailable")
	}
	if accountID <= 0 {
		return nil, fmt.Errorf("account_id is required")
	}
	existing, err := s.repo.ListByAccount(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("list existing account window members: %w", err)
	}
	if len(existing) > 0 {
		return nil, nil
	}
	reader, ok := s.repo.(peerSharedAccountMemberReader)
	if !ok {
		return nil, fmt.Errorf("%w: account=%d has no member discovery support", ErrAccountWindowNoActiveMembers, accountID)
	}
	userIDs, err := reader.ListPeerSharedAccountMemberUserIDs(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("list peer shared account members: %w", err)
	}
	if len(userIDs) == 0 {
		return nil, fmt.Errorf("%w: account=%d must inherit or explicitly configure members", ErrAccountWindowNoActiveMembers, accountID)
	}
	return s.SetAccountMembers(ctx, accountID, userIDs)
}

// SetAccountMembers 以管理员显式选择的用户列表同步拼车成员；保留 retained 成员状态，新成员只获得受剩余 ceiling 约束的安全默认额度。
func (s *AccountWindowQuotaService) SetAccountMembers(ctx context.Context, accountID int64, userIDs []int64) ([]AccountWindowEqualizationResult, error) {
	if s == nil || s.repo == nil || s.rdb == nil {
		return nil, errors.New("account window quota service unavailable")
	}
	if accountID <= 0 || len(userIDs) == 0 {
		return nil, fmt.Errorf("%w: account and at least one user are required", ErrAccountWindowInvalidMembers)
	}
	seen := make(map[int64]struct{}, len(userIDs))
	normalized := make([]int64, 0, len(userIDs))
	for _, userID := range userIDs {
		if userID <= 0 {
			return nil, fmt.Errorf("%w: invalid user id %d", ErrAccountWindowInvalidMembers, userID)
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		normalized = append(normalized, userID)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i] < normalized[j] })
	release, err := s.acquireConfigLock(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.repo.SyncAccountMembers(
		ctx,
		accountID,
		normalized,
		s.GetTotalCeiling(ctx, WindowType5h),
		s.GetTotalCeiling(ctx, WindowType7d),
	)
}

// SetDonateFraction 设置用户在某账号某窗口（5h/7d）的救急池捐赠比例（占自己该窗口上限，∈[0,1]）。
// 尚未被借走的额度可随时收回；已经被其他成员实际用掉的部分锁定到窗口重置，不能撤回。
func (s *AccountWindowQuotaService) SetDonateFraction(ctx context.Context, userID, accountID int64, window string, fraction float64) error {
	if s == nil || s.repo == nil {
		return nil
	}
	if userID <= 0 || accountID <= 0 {
		return fmt.Errorf("user_id and account_id are required")
	}
	if !IsValidWindowType(window) {
		return fmt.Errorf("invalid window type: %s", window)
	}
	fraction = clampFraction(fraction)
	records, err := s.repo.ListByAccount(ctx, accountID)
	if err != nil {
		return fmt.Errorf("list account window quotas before donation update: %w", err)
	}
	records = effectiveWindowQuotaRecordsAt(records, time.Now())
	isMember := false
	for _, record := range records {
		if record.UserID == userID && record.WindowType == window {
			isMember = true
			break
		}
	}
	if !isMember {
		return fmt.Errorf("%w: user=%d account=%d window=%s", ErrAccountWindowNotMember, userID, accountID, window)
	}
	if err := ValidateAccountWindowDonateFraction(records, userID, window, fraction); err != nil {
		return err
	}
	return s.repo.SetDonatePoolFraction(ctx, userID, accountID, window, fraction, s.defaultUserLimitPercent(ctx, window))
}

// UserWindowQuotaView 是用户侧展示用的窗口配额（含救急池信息）。
type UserWindowQuotaView struct {
	SharedPoolMode             bool
	AccountID                  int64
	WindowType                 string
	LimitPercent               float64 // 基础上限（本人 limit_percent）
	AttributedPercent          float64 // 已用百分点
	WindowResetAt              *time.Time
	DonateFraction             float64  // 本窗口救急池捐赠比例（5h/7d 各自独立）
	MinimumDonateFraction      float64  // 已借出额度锁定后的最低捐赠比例；窗口重置后回落为 0
	EffectiveLimitPercent      float64  // 当前实际可用上限：自留 /(基础 + 救急增量)
	PoolAvailablePercent       float64  // 该账号该窗口救急池当前可借总额
	AccountUsedPercent         *float64 // OpenAI 官方账号窗口已用百分比；缺失时为 nil，绝不从成员归因反推
	AccountAttributedPercent   *float64 // 当前活跃成员已归因之和
	AccountUnattributedPercent *float64 // 官方总量减成员已归因
	CeilingPercent             float64  // 该窗口账号总额上限（官方安全水位）
	ForceUnattributed          bool     // 账号处于「官方用量强制未归因」模式：成员归因恒为 0，官方用量主要来自站外
}

// ListUserWindowsWithPool 返回用户全部窗口配额并附带救急池信息（捐赠比例、有效上限、池剩余）。
func (s *AccountWindowQuotaService) ListUserWindowsWithPool(ctx context.Context, userID int64) ([]UserWindowQuotaView, error) {
	if s == nil || s.repo == nil {
		return nil, nil
	}
	records, err := s.repo.ListByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	records = effectiveWindowQuotaRecordsAt(records, now)
	ceiling := map[string]float64{
		WindowType5h: s.GetTotalCeiling(ctx, WindowType5h),
		WindowType7d: s.GetTotalCeiling(ctx, WindowType7d),
	}
	pvCache := map[int64]*accountWindowPoolView{}
	officialCache := map[string]struct {
		percent float64
		found   bool
	}{}
	forceCache := map[int64]bool{}
	resolveForce := func(accountID int64) bool {
		if force, ok := forceCache[accountID]; ok {
			return force
		}
		force := false
		if reader, ok := s.repo.(accountWindowForceUnattributedReader); ok {
			if value, err := reader.IsAccountWindowForceUnattributed(ctx, accountID); err == nil {
				force = value
			} else {
				slog.Warn("account_window_quota.read_force_unattributed_failed", "account_id", accountID, "error", err)
			}
		}
		forceCache[accountID] = force
		return force
	}
	views := make([]UserWindowQuotaView, 0, len(records))
	for _, r := range records {
		pv, ok := pvCache[r.AccountID]
		if !ok {
			if accRecords, aerr := s.repo.ListByAccount(ctx, r.AccountID); aerr != nil {
				slog.Warn("account_window_quota.list_account_failed", "account_id", r.AccountID, "error", aerr)
				pv = nil // 退化：救急池/账号级信息不可用时按基础上限展示
			} else {
				pv = buildAccountWindowPoolView(effectiveWindowQuotaRecordsAt(accRecords, now))
			}
			pvCache[r.AccountID] = pv
		}
		v := UserWindowQuotaView{
			SharedPoolMode:        r.SharedPoolMode,
			AccountID:             r.AccountID,
			WindowType:            r.WindowType,
			LimitPercent:          r.LimitPercent,
			AttributedPercent:     r.AttributedPercent,
			WindowResetAt:         r.WindowResetAt,
			EffectiveLimitPercent: r.LimitPercent,
			CeilingPercent:        ceiling[r.WindowType],
			ForceUnattributed:     resolveForce(r.AccountID),
		}
		officialKey := strconv.FormatInt(r.AccountID, 10) + ":" + r.WindowType
		official, cached := officialCache[officialKey]
		if !cached {
			official.percent, official.found = s.resolveAccountWindowOfficialPercent(ctx, r.AccountID, r.WindowType)
			officialCache[officialKey] = official
		}
		if official.found {
			value := official.percent
			v.AccountUsedPercent = &value
		}
		if pv != nil {
			switch r.WindowType {
			case WindowType5h:
				if pv.base5h[userID] > 0 {
					v.DonateFraction = pv.frac5h[userID]
					v.MinimumDonateFraction = pv.minimumDonateFraction(userID, r.WindowType)
					v.EffectiveLimitPercent = pv.effective5hLimit(userID)
					v.PoolAvailablePercent = pv.pool5hAvailable()
				}
			case WindowType7d:
				if pv.limit7d[userID] > 0 {
					v.DonateFraction = pv.frac7d[userID]
					v.MinimumDonateFraction = pv.minimumDonateFraction(userID, r.WindowType)
					v.EffectiveLimitPercent = pv.effective7dLimit(userID)
					v.PoolAvailablePercent = pv.pool7dAvailable()
				}
			}
			attributed := pv.sumAttributed(r.WindowType)
			v.AccountAttributedPercent = &attributed
			if official.found {
				unattributed := normalizeAccountWindowPercent(math.Max(0, official.percent-attributed))
				v.AccountUnattributedPercent = &unattributed
			}
		}
		views = append(views, v)
	}
	return views, nil
}
