package handler

import (
	"errors"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// AccountWindowQuotaHandler 暴露 user × account × window 百分比配额：
//   - 用户侧：查看自己在各账号各官方窗口的已用/剩余百分比与重置时间。
//   - 管理侧：覆盖某 (user, account, window) 的 limit_percent（默认 23%）。
type AccountWindowQuotaHandler struct {
	quota *service.AccountWindowQuotaService
}

// NewAccountWindowQuotaHandler 构造 AccountWindowQuotaHandler。
func NewAccountWindowQuotaHandler(quota *service.AccountWindowQuotaService) *AccountWindowQuotaHandler {
	return &AccountWindowQuotaHandler{quota: quota}
}

type accountWindowQuotaItem struct {
	SharedPoolMode             bool     `json:"shared_pool_mode"`
	AccountID                  int64    `json:"account_id"`
	WindowType                 string   `json:"window_type"`
	LimitPercent               float64  `json:"limit_percent"`
	UsedPercent                float64  `json:"used_percent"`
	RemainingPercent           float64  `json:"remaining_percent"`
	DonateFraction             float64  `json:"donate_fraction"`                        // 本窗口救急池捐赠比例（占自己份额）
	MinimumDonateFraction      float64  `json:"minimum_donate_fraction"`                // 已借出额度锁定后的最低捐赠比例
	EffectiveLimitPercent      float64  `json:"effective_limit_percent"`                // 当前实际可用上限（含救急池增量 / 捐赠自留约束）
	PoolAvailablePercent       float64  `json:"pool_available_percent"`                 // 该账号该窗口尚未被借走的救急池余额
	AccountUsedPercent         *float64 `json:"account_used_percent,omitempty"`         // OpenAI 官方账号窗口已用百分比；缺失时不返回
	AccountAttributedPercent   *float64 `json:"account_attributed_percent,omitempty"`   // 当前活跃成员已归因之和
	AccountUnattributedPercent *float64 `json:"account_unattributed_percent,omitempty"` // 官方总量减成员归因
	CeilingPercent             float64  `json:"ceiling_percent"`                        // 该窗口账号总额上限（官方安全水位）
	ForceUnattributed          bool     `json:"force_unattributed"`                     // 账号处于「官方用量强制未归因」模式
	WindowResetAt              *string  `json:"window_reset_at,omitempty"`
	ResetInSeconds             *int64   `json:"reset_in_seconds,omitempty"`
}

// GetMyWindows 返回当前登录用户的全部窗口配额（含救急池信息）。
// GET /api/v1/user/account-window-quotas
func (h *AccountWindowQuotaHandler) GetMyWindows(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if h.quota == nil || !h.quota.Enabled() {
		response.Success(c, gin.H{
			"enabled":   false,
			"windows":   []accountWindowQuotaItem{},
			"members":   []adminWindowQuotaOverviewItem{},
			"summaries": []adminWindowQuotaSummaryItem{},
		})
		return
	}
	views, err := h.quota.ListUserWindowsWithPool(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	memberRecords, summaryRecords, err := h.quota.ListSharedForUser(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{
		"enabled":   true,
		"windows":   buildWindowItemsFromViews(views, time.Now()),
		"members":   buildOverviewItems(memberRecords, time.Now()),
		"summaries": buildSummaryItems(summaryRecords),
	})
}

// buildWindowItemsFromViews 把含救急池信息的视图转成前端展示项（剩余按有效上限计）。
func buildWindowItemsFromViews(views []service.UserWindowQuotaView, now time.Time) []accountWindowQuotaItem {
	windows := make([]accountWindowQuotaItem, 0, len(views))
	for _, v := range views {
		remaining := v.EffectiveLimitPercent - v.AttributedPercent
		if remaining < 0 {
			remaining = 0
		}
		item := accountWindowQuotaItem{
			SharedPoolMode:             v.SharedPoolMode,
			AccountID:                  v.AccountID,
			WindowType:                 v.WindowType,
			LimitPercent:               v.LimitPercent,
			UsedPercent:                v.AttributedPercent,
			RemainingPercent:           remaining,
			DonateFraction:             v.DonateFraction,
			MinimumDonateFraction:      v.MinimumDonateFraction,
			EffectiveLimitPercent:      v.EffectiveLimitPercent,
			PoolAvailablePercent:       v.PoolAvailablePercent,
			AccountUsedPercent:         v.AccountUsedPercent,
			AccountAttributedPercent:   v.AccountAttributedPercent,
			AccountUnattributedPercent: v.AccountUnattributedPercent,
			CeilingPercent:             v.CeilingPercent,
			ForceUnattributed:          v.ForceUnattributed,
		}
		if v.WindowResetAt != nil {
			iso := v.WindowResetAt.UTC().Format(time.RFC3339)
			item.WindowResetAt = &iso
			secs := int64(v.WindowResetAt.Sub(now).Seconds())
			if secs < 0 {
				secs = 0
			}
			item.ResetInSeconds = &secs
		}
		windows = append(windows, item)
	}
	return windows
}

// AdminGetUserWindows 返回指定用户的全部窗口配额（管理端在用户配额弹窗中展示）。
// GET /api/v1/admin/account-window-quotas/users/:id
func (h *AccountWindowQuotaHandler) AdminGetUserWindows(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || userID <= 0 {
		response.BadRequest(c, "invalid user id")
		return
	}
	if h.quota == nil || !h.quota.Enabled() {
		response.Success(c, gin.H{"enabled": false, "windows": []accountWindowQuotaItem{}})
		return
	}
	views, err := h.quota.ListUserWindowsWithPool(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"enabled": true, "windows": buildWindowItemsFromViews(views, time.Now())})
}

type adminWindowQuotaOverviewItem struct {
	SharedPoolMode        bool    `json:"shared_pool_mode"`
	UserID                int64   `json:"user_id"`
	Email                 string  `json:"email"`
	Username              string  `json:"username"`
	AccountID             int64   `json:"account_id"`
	WindowType            string  `json:"window_type"`
	LimitPercent          float64 `json:"limit_percent"`
	UsedPercent           float64 `json:"used_percent"`
	RemainingPercent      float64 `json:"remaining_percent"`
	DonateFraction        float64 `json:"donate_fraction"`         // 本窗口救急池捐赠比例（5h/7d 各自独立）
	EffectiveLimitPercent float64 `json:"effective_limit_percent"` // 含救急池增量/捐赠自留后的有效上限
	PoolAvailablePercent  float64 `json:"pool_available_percent"`  // 该账号该窗口救急池当前可借总额
	WindowResetAt         *string `json:"window_reset_at,omitempty"`
	ResetInSeconds        *int64  `json:"reset_in_seconds,omitempty"`
}

type adminWindowQuotaSummaryItem struct {
	SharedPoolMode       bool     `json:"shared_pool_mode"`
	AccountID            int64    `json:"account_id"`
	WindowType           string   `json:"window_type"`
	MemberCount          int      `json:"member_count"`
	ConfiguredSumPercent float64  `json:"configured_sum_percent"`
	AttributedSumPercent float64  `json:"attributed_sum_percent"`
	OfficialUsedPercent  *float64 `json:"used_sum_percent,omitempty"` // 兼容原 JSON 字段名，值只来自 OpenAI 官方快照
	UnattributedPercent  *float64 `json:"unattributed_percent,omitempty"`
	CeilingPercent       float64  `json:"ceiling_percent"`
	Overallocated        bool     `json:"overallocated"`
}

func buildOverviewItems(records []service.AdminWindowQuotaOverviewRow, now time.Time) []adminWindowQuotaOverviewItem {
	rows := make([]adminWindowQuotaOverviewItem, 0, len(records))
	for _, r := range records {
		// EffectiveLimitPercent == 0 是合法值（全捐且未用的捐赠者自留上限为 0），
		// 不能回落到基础上限，否则前端会把已让出的份额显示成仍然可用。
		effLimit := r.EffectiveLimitPercent
		if effLimit < 0 {
			effLimit = 0
		}
		remaining := effLimit - r.AttributedPercent
		if remaining < 0 {
			remaining = 0
		}
		item := adminWindowQuotaOverviewItem{
			SharedPoolMode:        r.SharedPoolMode,
			UserID:                r.UserID,
			Email:                 r.Email,
			Username:              r.Username,
			AccountID:             r.AccountID,
			WindowType:            r.WindowType,
			LimitPercent:          r.LimitPercent,
			UsedPercent:           r.AttributedPercent,
			RemainingPercent:      remaining,
			DonateFraction:        r.DonatePoolFraction,
			EffectiveLimitPercent: effLimit,
			PoolAvailablePercent:  r.PoolAvailablePercent,
		}
		if r.WindowResetAt != nil {
			iso := r.WindowResetAt.UTC().Format(time.RFC3339)
			item.WindowResetAt = &iso
			secs := int64(r.WindowResetAt.Sub(now).Seconds())
			if secs < 0 {
				secs = 0
			}
			item.ResetInSeconds = &secs
		}
		rows = append(rows, item)
	}
	return rows
}

func buildSummaryItems(records []service.AdminWindowQuotaSummary) []adminWindowQuotaSummaryItem {
	summaries := make([]adminWindowQuotaSummaryItem, 0, len(records))
	for _, summary := range records {
		summaries = append(summaries, adminWindowQuotaSummaryItem{
			SharedPoolMode:       summary.SharedPoolMode,
			AccountID:            summary.AccountID,
			WindowType:           summary.WindowType,
			MemberCount:          summary.MemberCount,
			ConfiguredSumPercent: summary.ConfiguredSumPercent,
			AttributedSumPercent: summary.AttributedSumPercent,
			OfficialUsedPercent:  summary.OfficialUsedPercent,
			UnattributedPercent:  summary.UnattributedPercent,
			CeilingPercent:       summary.CeilingPercent,
			Overallocated:        summary.Overallocated,
		})
	}
	return summaries
}

// AdminOverview 返回所有用户在所有账号所有窗口的配额（带邮箱），供管理员仪表盘总览。
// GET /api/v1/admin/account-window-quotas/overview
func (h *AccountWindowQuotaHandler) AdminOverview(c *gin.Context) {
	if h.quota == nil || !h.quota.Enabled() {
		response.Success(c, gin.H{
			"enabled":     false,
			"rows":        []adminWindowQuotaOverviewItem{},
			"summaries":   []adminWindowQuotaSummaryItem{},
			"account_ids": []int64{},
		})
		return
	}
	records, summaryRecords, err := h.quota.ListAllForAdmin(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	accountIDs, err := h.quota.ListSharedAccountIDs(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	sharedPoolIDs, err := h.quota.ListSharedPoolAccountIDs(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{
		"enabled":                 true,
		"rows":                    buildOverviewItems(records, time.Now()),
		"summaries":               buildSummaryItems(summaryRecords),
		"account_ids":             accountIDs,
		"shared_pool_account_ids": sharedPoolIDs,
	})
}

// AdminSetSharedPoolMode manually enables or disables shared use of both windows.
// PUT /api/v1/admin/account-window-quotas/accounts/:id/shared-pool
func (h *AccountWindowQuotaHandler) AdminSetSharedPoolMode(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "invalid account id")
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
		response.BadRequest(c, "enabled must be a boolean")
		return
	}
	if h.quota == nil || !h.quota.Enabled() {
		response.BadRequest(c, "account window quota is not enabled")
		return
	}
	if err := h.quota.SetAccountSharedPoolMode(c.Request.Context(), accountID, *req.Enabled); err != nil {
		if errors.Is(err, service.ErrAccountWindowInvalidMembers) {
			response.BadRequest(c, "account is not an active shared account")
			return
		}
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"account_id": accountID, "shared_pool_mode": *req.Enabled})
}

type setAccountWindowLimitRequest struct {
	UserID       int64   `json:"user_id"`
	AccountID    int64   `json:"account_id"`
	WindowType   string  `json:"window_type"`
	LimitPercent float64 `json:"limit_percent"`
}

// AdminSetLimit 覆盖某 (user, account, window) 的 limit_percent。
// POST /api/v1/admin/account-window-quotas/limit
func (h *AccountWindowQuotaHandler) AdminSetLimit(c *gin.Context) {
	var req setAccountWindowLimitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	if req.UserID <= 0 || req.AccountID <= 0 {
		response.BadRequest(c, "user_id and account_id are required")
		return
	}
	if !service.IsValidWindowType(req.WindowType) {
		response.BadRequest(c, "window_type must be 5h or 7d")
		return
	}
	if req.LimitPercent < 0 || req.LimitPercent > 100 {
		response.BadRequest(c, "limit_percent must be between 0 and 100")
		return
	}
	if h.quota == nil || !h.quota.Enabled() {
		response.BadRequest(c, "account window quota is not enabled")
		return
	}
	if err := h.quota.SetLimitForUserAccount(c.Request.Context(), req.UserID, req.AccountID, req.WindowType, req.LimitPercent); err != nil {
		if errors.Is(err, service.ErrAccountWindowCeilingExceeded) || errors.Is(err, service.ErrAccountWindowInvalidMembers) {
			response.BadRequest(c, err.Error())
			return
		}
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"ok": true})
}

type equalizedAccountWindowItem struct {
	WindowType     string  `json:"window_type"`
	MemberCount    int     `json:"member_count"`
	SharePercent   float64 `json:"share_percent"`
	CeilingPercent float64 `json:"ceiling_percent"`
}

func buildEqualizedWindowItems(results []service.AccountWindowEqualizationResult) []equalizedAccountWindowItem {
	windows := make([]equalizedAccountWindowItem, 0, len(results))
	for _, result := range results {
		windows = append(windows, equalizedAccountWindowItem{
			WindowType:     result.WindowType,
			MemberCount:    result.ActiveMemberCount,
			SharePercent:   result.SharePercent,
			CeilingPercent: result.CeilingPercent,
		})
	}
	return windows
}

// AdminEqualizeAccountLimits 按指定账号各窗口当前活跃成员数均分 5h/7d ceiling。
// POST /api/v1/admin/account-window-quotas/accounts/:id/equalize
func (h *AccountWindowQuotaHandler) AdminEqualizeAccountLimits(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "invalid account id")
		return
	}
	if h.quota == nil || !h.quota.Enabled() {
		response.BadRequest(c, "account window quota is not enabled")
		return
	}
	results, err := h.quota.EqualizeAccountActiveMemberLimits(c.Request.Context(), accountID)
	if err != nil {
		if errors.Is(err, service.ErrAccountWindowNoActiveMembers) {
			response.BadRequest(c, err.Error())
			return
		}
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"ok": true, "account_id": accountID, "windows": buildEqualizedWindowItems(results)})
}

type setAccountWindowMembersRequest struct {
	UserIDs []int64 `json:"user_ids"`
}

// AdminSetAccountMembers 显式同步拼车账号成员；保留已有成员手工额度，新成员仅创建安全默认额度。
// PUT /api/v1/admin/account-window-quotas/accounts/:id/members
func (h *AccountWindowQuotaHandler) AdminSetAccountMembers(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "invalid account id")
		return
	}
	var req setAccountWindowMembersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	if h.quota == nil || !h.quota.Enabled() {
		response.BadRequest(c, "account window quota is not enabled")
		return
	}
	results, err := h.quota.SetAccountMembers(c.Request.Context(), accountID, req.UserIDs)
	if err != nil {
		if errors.Is(err, service.ErrAccountWindowInvalidMembers) {
			response.BadRequest(c, err.Error())
			return
		}
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"ok": true, "account_id": accountID, "windows": buildEqualizedWindowItems(results)})
}

type accountWindowCeilingItem struct {
	WindowType     string  `json:"window_type"`
	CeilingPercent float64 `json:"ceiling_percent"`
}

// AdminGetCeilings 返回各官方窗口的总额上限（所有用户 limit 之和的上限）。
// GET /api/v1/admin/account-window-quotas/ceilings
func (h *AccountWindowQuotaHandler) AdminGetCeilings(c *gin.Context) {
	if h.quota == nil || !h.quota.Enabled() {
		response.Success(c, gin.H{"enabled": false, "ceilings": []accountWindowCeilingItem{}})
		return
	}
	ctx := c.Request.Context()
	ceilings := []accountWindowCeilingItem{
		{WindowType: service.WindowType5h, CeilingPercent: h.quota.GetTotalCeiling(ctx, service.WindowType5h)},
		{WindowType: service.WindowType7d, CeilingPercent: h.quota.GetTotalCeiling(ctx, service.WindowType7d)},
	}
	seats := h.quota.GetSeats(ctx)
	response.Success(c, gin.H{
		"enabled":  true,
		"ceilings": ceilings,
		"seats":    seats,
		// 人均默认上限 = ceiling/seats（仅作展示提示，自动建行时用）。
		"default_limit_percent": h.quota.GetTotalCeiling(ctx, service.WindowType5h) / float64(seats),
	})
}

type setAccountWindowSeatsRequest struct {
	Seats int `json:"seats"`
}

// AdminSetSeats 设置车位数（共享人数）。换 3/5/8 人车只改这个，人均默认 = ceiling/seats 自动适配。
// POST /api/v1/admin/account-window-quotas/seats
func (h *AccountWindowQuotaHandler) AdminSetSeats(c *gin.Context) {
	var req setAccountWindowSeatsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	if req.Seats <= 0 || req.Seats > 100 {
		response.BadRequest(c, "seats must be between 1 and 100")
		return
	}
	if h.quota == nil || !h.quota.Enabled() {
		response.BadRequest(c, "account window quota is not enabled")
		return
	}
	if err := h.quota.SetSeats(c.Request.Context(), req.Seats); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"ok": true})
}

type setAccountWindowCeilingRequest struct {
	WindowType     string  `json:"window_type"`
	CeilingPercent float64 `json:"ceiling_percent"`
}

// AdminSetCeiling 设置某官方窗口的总额上限（运行时可配）。
// POST /api/v1/admin/account-window-quotas/ceiling
func (h *AccountWindowQuotaHandler) AdminSetCeiling(c *gin.Context) {
	var req setAccountWindowCeilingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	if !service.IsValidWindowType(req.WindowType) {
		response.BadRequest(c, "window_type must be 5h or 7d")
		return
	}
	if req.CeilingPercent <= 0 || req.CeilingPercent > 100 {
		response.BadRequest(c, "ceiling_percent must be between 0 and 100")
		return
	}
	if h.quota == nil || !h.quota.Enabled() {
		response.BadRequest(c, "account window quota is not enabled")
		return
	}
	if err := h.quota.SetTotalCeiling(c.Request.Context(), req.WindowType, req.CeilingPercent); err != nil {
		if errors.Is(err, service.ErrAccountWindowCeilingBelowConfigured) {
			response.BadRequest(c, err.Error())
			return
		}
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"ok": true})
}

type setDonateFractionRequest struct {
	AccountID  int64   `json:"account_id"`
	WindowType string  `json:"window_type"` // '5h' 或 '7d'；缺省按 '5h'（向后兼容）
	Fraction   float64 `json:"fraction"`    // 占自己该窗口份额的捐赠比例，∈[0,1]
}

// Donate 设置当前登录用户在某账号某窗口（5h/7d）的救急池捐赠比例（自助、自愿）。
// POST /api/v1/user/account-window-quotas/donate
func (h *AccountWindowQuotaHandler) Donate(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	var req setDonateFractionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	if req.AccountID <= 0 {
		response.BadRequest(c, "account_id is required")
		return
	}
	if req.WindowType == "" {
		req.WindowType = service.WindowType5h
	}
	if !service.IsValidWindowType(req.WindowType) {
		response.BadRequest(c, "window_type must be 5h or 7d")
		return
	}
	if req.Fraction < 0 || req.Fraction > 1 {
		response.BadRequest(c, "fraction must be between 0 and 1")
		return
	}
	if h.quota == nil || !h.quota.Enabled() {
		response.BadRequest(c, "account window quota is not enabled")
		return
	}
	if err := h.quota.SetDonateFraction(c.Request.Context(), subject.UserID, req.AccountID, req.WindowType, req.Fraction); err != nil {
		if errors.Is(err, service.ErrAccountWindowDonationInUse) || errors.Is(err, service.ErrAccountWindowNotMember) {
			response.BadRequest(c, err.Error())
			return
		}
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"ok": true})
}
