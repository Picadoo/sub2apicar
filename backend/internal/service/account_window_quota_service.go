package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// 官方窗口类型常量。
const (
	WindowType5h = "5h"
	WindowType7d = "7d"

	// DefaultAccountWindowLimitPercent 是单用户在某账号某窗口可占用的官方利用率百分点默认上限。
	// 4 人共享：4×23%=92%，留 ~8% 安全缓冲，避免占满官方窗口触发风控。
	DefaultAccountWindowLimitPercent = 23.0

	// DefaultAccountWindowTotalCeilingPercent 是某账号某窗口下「所有用户 limit 之和」的默认总额上限。
	// 4×23%=92%，留 ~8% 安全缓冲。管理端设置单用户 limit 时不得令该窗口各用户之和超过此上限。
	// 运行时可通过 SetTotalCeiling 覆盖（存 Redis，无 TTL），缺省回落到本默认值。
	DefaultAccountWindowTotalCeilingPercent = 92.0

	// accountWindowBaselineTTLSeconds 是 Redis 基线键存活时间（8 天 > 最长 7d 窗口）。
	accountWindowBaselineTTLSeconds = 8 * 24 * 60 * 60

	// 官方窗口时长（秒），用于推算"本窗口起点" = 重置时间 − 窗口时长。
	window5hLengthSeconds = 5 * 60 * 60
	window7dLengthSeconds = 7 * 24 * 60 * 60
)

// windowLengthSeconds 返回某官方窗口的时长（秒）。
func windowLengthSeconds(window string) int {
	if window == WindowType7d {
		return window7dLengthSeconds
	}
	return window5hLengthSeconds
}

// ErrAccountWindowCeilingExceeded 表示设置某用户 limit 会令该窗口所有用户之和超过总额上限。
var ErrAccountWindowCeilingExceeded = errors.New("account window total ceiling exceeded")

// accountWindowTypes 是需要分摊/校验的全部官方窗口。
var accountWindowTypes = []string{WindowType5h, WindowType7d}

// UserAccountWindowQuotaRecord 是 user × account × window 维度配额台账的传输结构体。
type UserAccountWindowQuotaRecord struct {
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
	UserID             int64
	Email              string
	Username           string
	AccountID          int64
	WindowType         string
	LimitPercent       float64
	AttributedPercent  float64
	WindowResetAt      *time.Time
	DonatePoolFraction float64
}

// AccountWindowReset 标识一个待重置的 (账号, 窗口) 组合。
type AccountWindowReset struct {
	AccountID  int64
	WindowType string
}

// UserAccountWindowQuotaRepository 定义 user × account × window 配额台账的数据访问接口。
type UserAccountWindowQuotaRepository interface {
	// AddAttributedPercent 累加 delta 到 (user, account, window) 的 attributed_percent。
	// 行不存在时插入（limit_percent=defaultLimit，attributed_percent=delta）；
	// 存在时累加并以 COALESCE 更新 window_reset_at（不覆盖已有 limit_percent）。
	AddAttributedPercent(ctx context.Context, userID, accountID int64, window string, delta float64, resetAt *time.Time, defaultLimit float64) error
	// ResetWindowForAccount 把某账号某窗口下所有活跃用户的 attributed_percent 清零，并刷新 window_reset_at。
	ResetWindowForAccount(ctx context.Context, accountID int64, window string, newResetAt *time.Time) error
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
	SetDonatePoolFraction(ctx context.Context, userID, accountID int64, window string, fraction float64) error
	// RecomputeWindowShares 按"本窗口内各用户实际 token 占比"重算该账号该窗口下所有有用量用户的
	// attributed_percent = officialPct × (该用户 token / 全部 token)。幂等：从 usage_logs 权威重算，
	// 不累加、不受调用次数影响（不会重复计数）。windowStart 之后（含）的 usage_logs 计入当前窗口。
	RecomputeWindowShares(ctx context.Context, accountID int64, window string, officialPct float64, windowStart time.Time, resetAt *time.Time, defaultLimit float64) error
	// ListAllWithUser 返回所有活跃配额行（带用户邮箱/用户名），供管理端总览。
	ListAllWithUser(ctx context.Context) ([]AdminWindowQuotaOverviewRow, error)
}

// accountWindowBaselineScript 原子地读取/更新某 (账号, 窗口) 的"上次观测到的官方利用率"基线，
// 返回 {delta, resetFlag}：
//   - 首次观测（基线缺失）：只播种基线，delta=0（避免重启后把累计用量算到某个倒霉用户头上）。
//   - 新值显著低于基线（官方窗口刷新/回落）：resetFlag=1，delta=新值（新窗口里的现有用量）。
//   - 正常增长：delta = 新值 - 基线。
const accountWindowBaselineScript = `
local cur = redis.call('GET', KEYS[1])
local newp = tonumber(ARGV[1])
if newp == nil then return {'0', 0} end
local delta = 0
local reset = 0
if cur ~= false then
  local c = tonumber(cur)
  if c ~= nil then
    if newp + 0.0001 < c then
      reset = 1
      delta = newp
    else
      delta = newp - c
    end
  end
end
redis.call('SET', KEYS[1], ARGV[1])
local ttl = tonumber(ARGV[2])
if ttl ~= nil and ttl > 0 then redis.call('EXPIRE', KEYS[1], ttl) end
return {tostring(delta), reset}
`

// AccountWindowQuotaService 负责把账号级官方利用率增量分摊到发起请求的用户，
// 并提供 23% 配额校验与窗口重置能力。
type AccountWindowQuotaService struct {
	repo UserAccountWindowQuotaRepository
	rdb  *redis.Client
}

// NewAccountWindowQuotaService 构造 AccountWindowQuotaService。
func NewAccountWindowQuotaService(repo UserAccountWindowQuotaRepository, rdb *redis.Client) *AccountWindowQuotaService {
	return &AccountWindowQuotaService{repo: repo, rdb: rdb}
}

// Enabled 报告服务是否具备分摊/校验所需依赖。
func (s *AccountWindowQuotaService) Enabled() bool {
	return s != nil && s.repo != nil && s.rdb != nil
}

func accountWindowBaselineKey(accountID int64, window string) string {
	return "uawq:base:" + strconv.FormatInt(accountID, 10) + ":" + window
}

func accountWindowCeilingKey(window string) string {
	return "uawq:ceiling:" + window
}

// Attribute 把本次响应携带的账号级官方利用率快照增量分摊给发起请求的用户。
// best-effort：任何错误只记日志、不影响请求主流程。
func (s *AccountWindowQuotaService) Attribute(ctx context.Context, userID, accountID int64, snapshot *OpenAICodexUsageSnapshot) {
	if !s.Enabled() || userID <= 0 || accountID <= 0 || snapshot == nil {
		return
	}
	norm := snapshot.Normalize()
	if norm == nil {
		return
	}
	now := time.Now()
	s.attributeWindow(ctx, userID, accountID, WindowType5h, norm.Used5hPercent, norm.Reset5hSeconds, now)
	s.attributeWindow(ctx, userID, accountID, WindowType7d, norm.Used7dPercent, norm.Reset7dSeconds, now)
}

func (s *AccountWindowQuotaService) attributeWindow(ctx context.Context, userID, accountID int64, window string, usedPercent *float64, resetSeconds *int, now time.Time) {
	if usedPercent == nil {
		return
	}
	officialPercent := *usedPercent
	if officialPercent < 0 {
		officialPercent = 0
	}

	var resetAt *time.Time
	if resetSeconds != nil && *resetSeconds >= 0 {
		t := now.Add(time.Duration(*resetSeconds) * time.Second)
		resetAt = &t
	}

	// observeBaseline 现在只用于"官方窗口回落/刷新"检测；归因不再依赖它的 delta。
	if _, isReset, err := s.observeBaseline(ctx, accountID, window, officialPercent); err != nil {
		slog.Warn("account_window_quota.baseline_failed", "account_id", accountID, "window", window, "error", err)
	} else if isReset {
		// 官方窗口刷新：先把该账号该窗口下所有用户清零并刷新重置时间，清掉上一窗口的归因。
		if err := s.repo.ResetWindowForAccount(ctx, accountID, window, resetAt); err != nil {
			slog.Warn("account_window_quota.reset_failed", "account_id", accountID, "window", window, "error", err)
		}
	}

	// 公平归因：按"本窗口内各用户实际 token 占比"把账号级官方利用率分摊到每个用户。
	// 幂等——每次都从 usage_logs 权威重算，不累加、不受调用次数影响，
	// 也不会把官方整数百分比的一次跳变全记到"恰好观测到的那个倒霉用户"头上。
	windowStart := now.Add(-time.Duration(windowLengthSeconds(window)) * time.Second)
	if resetAt != nil {
		windowStart = resetAt.Add(-time.Duration(windowLengthSeconds(window)) * time.Second)
	}
	if err := s.repo.RecomputeWindowShares(ctx, accountID, window, officialPercent, windowStart, resetAt, DefaultAccountWindowLimitPercent); err != nil {
		slog.Warn("account_window_quota.recompute_failed", "account_id", accountID, "window", window, "error", err)
	}
}

// observeBaseline 调用 Lua 脚本原子地比较并更新基线，返回 (delta, isReset, error)。
func (s *AccountWindowQuotaService) observeBaseline(ctx context.Context, accountID int64, window string, newPercent float64) (float64, bool, error) {
	res, err := s.rdb.Eval(ctx, accountWindowBaselineScript,
		[]string{accountWindowBaselineKey(accountID, window)},
		strconv.FormatFloat(newPercent, 'f', -1, 64),
		accountWindowBaselineTTLSeconds,
	).Result()
	if err != nil {
		return 0, false, err
	}
	arr, ok := res.([]any)
	if !ok || len(arr) < 2 {
		return 0, false, nil
	}
	deltaStr, _ := arr[0].(string)
	delta, _ := strconv.ParseFloat(deltaStr, 64)
	if delta < 0 {
		delta = 0
	}
	isReset := false
	switch v := arr[1].(type) {
	case int64:
		isReset = v == 1
	case string:
		isReset = v == "1"
	}
	return delta, isReset, nil
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

// within7d 报告用户 7d 周配额是否仍有余量（无 7d 记录或上限<=0 视为不限）。
func (pv *accountWindowPoolView) within7d(userID int64) bool {
	limit, ok := pv.limit7d[userID]
	if !ok || limit <= 0 {
		return true
	}
	return pv.used7d[userID]+windowQuotaEpsilon < limit
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

// pool5hAvailable 返回 5h 救急池当前可借总额 = Σ 捐赠者(基础上限 − 自留有效上限)。
func (pv *accountWindowPoolView) pool5hAvailable() float64 {
	pool := 0.0
	for userID, frac := range pv.frac5h {
		if frac <= 0 {
			continue
		}
		if donated := pv.base5h[userID] - pv.donor5hKeepCap(userID); donated > 0 {
			pool += donated
		}
	}
	return pool
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
	pool := pv.pool5hAvailable()
	if needy <= 0 || pool <= 0 {
		return base
	}
	return base + pool/float64(needy)
}

// sumUsed 返回某窗口下账号全员已用百分点之和（账号级官方利用率）。
func (pv *accountWindowPoolView) sumUsed(window string) float64 {
	m := pv.used5h
	if window == WindowType7d {
		m = pv.used7d
	}
	sum := 0.0
	for _, v := range m {
		sum += v
	}
	return sum
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

// pool7dAvailable 返回 7d 救急池当前可借总额 = Σ 自愿捐赠者(基础上限 − 自留有效上限)。
func (pv *accountWindowPoolView) pool7dAvailable() float64 {
	pool := 0.0
	for userID, frac := range pv.frac7d {
		if frac <= 0 {
			continue
		}
		if donated := pv.limit7d[userID] - pv.donor7dKeepCap(userID); donated > 0 {
			pool += donated
		}
	}
	return pool
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
	pool := pv.pool7dAvailable()
	if needy <= 0 || pool <= 0 {
		return base
	}
	return base + pool/float64(needy)
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
	ceiling7d := s.GetTotalCeiling(ctx, WindowType7d)
	ceiling5h := s.GetTotalCeiling(ctx, WindowType5h)

	// 0) 账号级硬闸（与人数无关的最终安全网，只"封顶"不"转移份额"，非强制分配）：
	//    某窗口全员已用之和达到官方安全水位 → 全员拦截，无论个人是否还有余量。
	//    这是 3/5/8 人车也成立的护栏，哪怕 per-user 配额配错也绝不会把账号推过 ceiling 触发风控。
	if pv.sumUsed(WindowType7d)+windowQuotaEpsilon >= ceiling7d {
		return false, WindowType7d, pv.reset7d[userID]
	}
	if pv.sumUsed(WindowType5h)+windowQuotaEpsilon >= ceiling5h {
		return false, WindowType5h, pv.reset5h[userID]
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
	due, err := s.repo.ListDueResets(ctx, time.Now())
	if err != nil {
		slog.Warn("account_window_quota.list_due_resets_failed", "error", err)
		return
	}
	for _, d := range due {
		if err := s.repo.ResetWindowForAccount(ctx, d.AccountID, d.WindowType, nil); err != nil {
			slog.Warn("account_window_quota.timed_reset_failed", "account_id", d.AccountID, "window", d.WindowType, "error", err)
		}
	}
}

// ListUserWindows 返回用户的全部窗口配额（供前端展示）。
func (s *AccountWindowQuotaService) ListUserWindows(ctx context.Context, userID int64) ([]UserAccountWindowQuotaRecord, error) {
	if s == nil || s.repo == nil {
		return nil, nil
	}
	return s.repo.ListByUser(ctx, userID)
}

// ListAllForAdmin 返回所有用户在所有账号所有窗口的配额（带用户信息，管理端总览用）。
func (s *AccountWindowQuotaService) ListAllForAdmin(ctx context.Context) ([]AdminWindowQuotaOverviewRow, error) {
	if s == nil || s.repo == nil {
		return nil, nil
	}
	return s.repo.ListAllWithUser(ctx)
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
func (s *AccountWindowQuotaService) SetTotalCeiling(ctx context.Context, window string, percent float64) error {
	if s == nil || s.rdb == nil {
		return errors.New("account window quota service unavailable")
	}
	if !IsValidWindowType(window) {
		return fmt.Errorf("invalid window type: %s", window)
	}
	if percent <= 0 || percent > 100 {
		return fmt.Errorf("ceiling percent must be in (0,100], got %.2f", percent)
	}
	return s.rdb.Set(ctx, accountWindowCeilingKey(window), strconv.FormatFloat(percent, 'f', -1, 64), 0).Err()
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
	ceiling := s.GetTotalCeiling(ctx, window)
	// 读不到其余用户配额时 fail-open（不因瞬时读错误阻断配置）。
	if records, err := s.repo.ListByAccount(ctx, accountID); err == nil {
		sumOthers := 0.0
		for _, r := range records {
			if r.WindowType == window && r.UserID != userID {
				sumOthers += r.LimitPercent
			}
		}
		if sumOthers+limitPercent > ceiling+1e-6 {
			return fmt.Errorf("%w: account=%d window=%s 其余用户之和 %.2f%% + 本次 %.2f%% 超过上限 %.2f%%",
				ErrAccountWindowCeilingExceeded, accountID, window, sumOthers, limitPercent, ceiling)
		}
	}
	return s.repo.SetLimitForUserAccount(ctx, userID, accountID, window, limitPercent)
}

// SetDonateFraction 设置用户在某账号某窗口（5h/7d）的救急池捐赠比例（占自己该窗口上限，∈[0,1]）。
// 自助操作：用户自愿把暂时不用的份额让给该窗口救急池，自留部分永远即时可用；越界自动夹紧到 [0,1]。
// 全程自愿——不捐（fraction=0）即恢复成谁也动不了的硬上限。
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
	return s.repo.SetDonatePoolFraction(ctx, userID, accountID, window, clampFraction(fraction))
}

// UserWindowQuotaView 是用户侧展示用的窗口配额（含救急池信息）。
type UserWindowQuotaView struct {
	AccountID             int64
	WindowType            string
	LimitPercent          float64 // 基础上限（本人 limit_percent）
	AttributedPercent     float64 // 已用百分点
	WindowResetAt         *time.Time
	DonateFraction        float64 // 本窗口救急池捐赠比例（5h/7d 各自独立）
	EffectiveLimitPercent float64 // 当前实际可用上限：自留 /(基础 + 救急增量)
	PoolAvailablePercent  float64 // 该账号该窗口救急池当前可借总额
	AccountUsedPercent    float64 // 该账号该窗口全员已用之和（账号级利用率）
	CeilingPercent        float64 // 该窗口账号总额上限（官方安全水位）
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
	ceiling := map[string]float64{
		WindowType5h: s.GetTotalCeiling(ctx, WindowType5h),
		WindowType7d: s.GetTotalCeiling(ctx, WindowType7d),
	}
	pvCache := map[int64]*accountWindowPoolView{}
	views := make([]UserWindowQuotaView, 0, len(records))
	for _, r := range records {
		pv, ok := pvCache[r.AccountID]
		if !ok {
			if accRecords, aerr := s.repo.ListByAccount(ctx, r.AccountID); aerr != nil {
				slog.Warn("account_window_quota.list_account_failed", "account_id", r.AccountID, "error", aerr)
				pv = nil // 退化：救急池/账号级信息不可用时按基础上限展示
			} else {
				pv = buildAccountWindowPoolView(accRecords)
			}
			pvCache[r.AccountID] = pv
		}
		v := UserWindowQuotaView{
			AccountID:             r.AccountID,
			WindowType:            r.WindowType,
			LimitPercent:          r.LimitPercent,
			AttributedPercent:     r.AttributedPercent,
			WindowResetAt:         r.WindowResetAt,
			EffectiveLimitPercent: r.LimitPercent,
			CeilingPercent:        ceiling[r.WindowType],
		}
		if pv != nil {
			v.AccountUsedPercent = pv.sumUsed(r.WindowType)
			switch r.WindowType {
			case WindowType5h:
				if pv.base5h[userID] > 0 {
					v.DonateFraction = pv.frac5h[userID]
					v.EffectiveLimitPercent = pv.effective5hLimit(userID)
					v.PoolAvailablePercent = pv.pool5hAvailable()
				}
			case WindowType7d:
				if pv.limit7d[userID] > 0 {
					v.DonateFraction = pv.frac7d[userID]
					v.EffectiveLimitPercent = pv.effective7dLimit(userID)
					v.PoolAvailablePercent = pv.pool7dAvailable()
				}
			}
		}
		views = append(views, v)
	}
	return views, nil
}
