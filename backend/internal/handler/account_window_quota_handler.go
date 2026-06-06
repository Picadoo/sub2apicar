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
	AccountID             int64   `json:"account_id"`
	WindowType            string  `json:"window_type"`
	LimitPercent          float64 `json:"limit_percent"`
	UsedPercent           float64 `json:"used_percent"`
	RemainingPercent      float64 `json:"remaining_percent"`
	DonateFraction        float64 `json:"donate_fraction"`         // 5h 救急池捐赠比例（占自己份额）；7d 恒 0
	EffectiveLimitPercent float64 `json:"effective_limit_percent"` // 当前实际可用上限（含救急池增量 / 捐赠自留约束）
	PoolAvailablePercent  float64 `json:"pool_available_percent"`  // 该账号 5h 救急池当前可借总额；7d 恒 0
	AccountUsedPercent    float64 `json:"account_used_percent"`    // 该账号该窗口全员已用之和（账号级利用率）
	CeilingPercent        float64 `json:"ceiling_percent"`         // 该窗口账号总额上限（官方安全水位）
	WindowResetAt         *string `json:"window_reset_at,omitempty"`
	ResetInSeconds        *int64  `json:"reset_in_seconds,omitempty"`
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
		response.Success(c, gin.H{"enabled": false, "windows": []accountWindowQuotaItem{}})
		return
	}
	views, err := h.quota.ListUserWindowsWithPool(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"enabled": true, "windows": buildWindowItemsFromViews(views, time.Now())})
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
			AccountID:             v.AccountID,
			WindowType:            v.WindowType,
			LimitPercent:          v.LimitPercent,
			UsedPercent:           v.AttributedPercent,
			RemainingPercent:      remaining,
			DonateFraction:        v.DonateFraction,
			EffectiveLimitPercent: v.EffectiveLimitPercent,
			PoolAvailablePercent:  v.PoolAvailablePercent,
			AccountUsedPercent:    v.AccountUsedPercent,
			CeilingPercent:        v.CeilingPercent,
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

// buildWindowItems 把配额台账记录转成展示项（管理端按用户查看，不含救急池信息）。
func buildWindowItems(records []service.UserAccountWindowQuotaRecord, now time.Time) []accountWindowQuotaItem {
	windows := make([]accountWindowQuotaItem, 0, len(records))
	for _, r := range records {
		remaining := r.LimitPercent - r.AttributedPercent
		if remaining < 0 {
			remaining = 0
		}
		item := accountWindowQuotaItem{
			AccountID:             r.AccountID,
			WindowType:            r.WindowType,
			LimitPercent:          r.LimitPercent,
			UsedPercent:           r.AttributedPercent,
			RemainingPercent:      remaining,
			DonateFraction:        r.DonatePoolFraction,
			EffectiveLimitPercent: r.LimitPercent,
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
	records, err := h.quota.ListUserWindows(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"enabled": true, "windows": buildWindowItems(records, time.Now())})
}

type adminWindowQuotaOverviewItem struct {
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

// AdminOverview 返回所有用户在所有账号所有窗口的配额（带邮箱），供管理员仪表盘总览。
// GET /api/v1/admin/account-window-quotas/overview
func (h *AccountWindowQuotaHandler) AdminOverview(c *gin.Context) {
	if h.quota == nil || !h.quota.Enabled() {
		response.Success(c, gin.H{"enabled": false, "rows": []adminWindowQuotaOverviewItem{}})
		return
	}
	records, err := h.quota.ListAllForAdmin(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	now := time.Now()
	rows := make([]adminWindowQuotaOverviewItem, 0, len(records))
	for _, r := range records {
		effLimit := r.EffectiveLimitPercent
		if effLimit <= 0 {
			effLimit = r.LimitPercent
		}
		remaining := effLimit - r.AttributedPercent
		if remaining < 0 {
			remaining = 0
		}
		item := adminWindowQuotaOverviewItem{
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
	response.Success(c, gin.H{"enabled": true, "rows": rows})
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
		if errors.Is(err, service.ErrAccountWindowCeilingExceeded) {
			response.BadRequest(c, err.Error())
			return
		}
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"ok": true})
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
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"ok": true})
}
