package handler

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// These response types deliberately contain only display-safe account metadata.
// Credentials and the account Extra map must never be serialized into /v1/usage.
type keyUsageAccountGroup struct {
	ID           int64             `json:"id"`
	Name         string            `json:"name"`
	Platform     string            `json:"platform"`
	AccountCount int               `json:"account_count"`
	Accounts     []keyUsageAccount `json:"accounts"`
}

type keyUsageAccount struct {
	ID          int64                            `json:"id"`
	Name        string                           `json:"name"`
	Email       string                           `json:"email,omitempty"`
	Platform    string                           `json:"platform"`
	Type        string                           `json:"type"`
	Status      string                           `json:"status"`
	Schedulable bool                             `json:"schedulable"`
	Shared      bool                             `json:"shared"`
	Windows     map[string]keyUsageAccountWindow `json:"windows,omitempty"`
	LocalQuota  *keyUsageLocalQuota              `json:"local_quota,omitempty"`
}

type keyUsageAccountWindow struct {
	OfficialUsedPercent         *float64   `json:"official_used_percent,omitempty"`
	OfficialRemainingPercent    *float64   `json:"official_remaining_percent,omitempty"`
	OfficialUnattributedPercent *float64   `json:"official_unattributed_percent,omitempty"`
	OfficialResetAt             *time.Time `json:"official_reset_at,omitempty"`
	OfficialObservedAt          *time.Time `json:"official_observed_at,omitempty"`

	UserLimitPercent          *float64   `json:"user_limit_percent,omitempty"`
	UserEffectiveLimitPercent *float64   `json:"user_effective_limit_percent,omitempty"`
	UserUsedPercent           *float64   `json:"user_used_percent,omitempty"`
	UserRemainingPercent      *float64   `json:"user_remaining_percent,omitempty"`
	UserPoolAvailablePercent  *float64   `json:"user_pool_available_percent,omitempty"`
	UserResetAt               *time.Time `json:"user_reset_at,omitempty"`
	UserForceUnattributed     bool       `json:"user_force_unattributed,omitempty"`
	UserSharedPoolMode        bool       `json:"user_shared_pool_mode,omitempty"`
}

type keyUsageLocalQuota struct {
	Total  *keyUsageQuotaDimension `json:"total,omitempty"`
	Daily  *keyUsageQuotaDimension `json:"daily,omitempty"`
	Weekly *keyUsageQuotaDimension `json:"weekly,omitempty"`
}

type keyUsageQuotaDimension struct {
	Limit     float64    `json:"limit"`
	Used      float64    `json:"used"`
	Remaining float64    `json:"remaining"`
	ResetAt   *time.Time `json:"reset_at,omitempty"`
	Unit      string     `json:"unit"`
}

// appendAPIKeyAccountGroups adds the account-pool view only for the page-specific
// query flag. Existing CC Switch clients keep the original response shape.
func (h *GatewayHandler) appendAPIKeyAccountGroups(c *gin.Context, ctx context.Context, apiKey *service.APIKey, userID int64, resp gin.H) {
	if c == nil || c.Query("include_accounts") != "true" || h.accountRepo == nil || apiKey == nil {
		return
	}

	groupID := int64(0)
	if apiKey.GroupID != nil {
		groupID = *apiKey.GroupID
	}
	if groupID <= 0 && apiKey.Group != nil {
		groupID = apiKey.Group.ID
	}
	if groupID <= 0 {
		return
	}

	accounts, err := h.accountRepo.ListByGroup(ctx, groupID)
	if err != nil {
		logger.LegacyPrintf("handler.gateway.usage", "failed to load account pool for group %d: %v", groupID, err)
		return
	}

	userWindows := make(map[int64]map[string]service.UserWindowQuotaView)
	if userID > 0 && h.accountWindowQuotaService != nil && h.accountWindowQuotaService.Enabled() {
		views, viewErr := h.accountWindowQuotaService.ListUserWindowsWithPool(ctx, userID)
		if viewErr != nil {
			logger.LegacyPrintf("handler.gateway.usage", "failed to load account window quota for user %d: %v", userID, viewErr)
		} else {
			for _, view := range views {
				if userWindows[view.AccountID] == nil {
					userWindows[view.AccountID] = make(map[string]service.UserWindowQuotaView)
				}
				userWindows[view.AccountID][view.WindowType] = view
			}
		}
	}

	groupName := "Group #" + strconv.FormatInt(groupID, 10)
	groupPlatform := ""
	if apiKey.Group != nil {
		if strings.TrimSpace(apiKey.Group.Name) != "" {
			groupName = apiKey.Group.Name
		}
		groupPlatform = apiKey.Group.Platform
	}

	now := time.Now()
	items := make([]keyUsageAccount, 0, len(accounts))
	for i := range accounts {
		items = append(items, buildKeyUsageAccount(&accounts[i], userWindows[accounts[i].ID], now))
	}

	resp["account_groups"] = []keyUsageAccountGroup{{
		ID:           groupID,
		Name:         groupName,
		Platform:     groupPlatform,
		AccountCount: len(items),
		Accounts:     items,
	}}
}

func buildKeyUsageAccount(account *service.Account, userWindows map[string]service.UserWindowQuotaView, now time.Time) keyUsageAccount {
	item := keyUsageAccount{
		Windows: make(map[string]keyUsageAccountWindow),
	}
	if account == nil {
		item.Windows = nil
		return item
	}

	item.ID = account.ID
	item.Name = account.Name
	item.Email = keyUsageAccountEmail(account)
	item.Platform = account.Platform
	item.Type = account.Type
	item.Status = account.Status
	item.Schedulable = account.Schedulable
	item.Shared = account.IsWindowQuotaShared()

	for _, windowType := range []string{service.WindowType5h, service.WindowType7d} {
		window, found := buildStoredAccountWindow(account, windowType, now)
		if view, ok := userWindows[windowType]; ok {
			if !found {
				window = &keyUsageAccountWindow{}
				found = true
			}
			applyUserWindowView(window, view)
		}
		if found {
			item.Windows[windowType] = *window
		}
	}
	if len(item.Windows) == 0 {
		item.Windows = nil
	}
	item.LocalQuota = buildLocalAccountQuota(account, now)
	return item
}

func applyUserWindowView(window *keyUsageAccountWindow, view service.UserWindowQuotaView) {
	if window == nil {
		return
	}

	if view.AccountUsedPercent != nil {
		used := clampKeyUsagePercent(*view.AccountUsedPercent)
		window.OfficialUsedPercent = &used
		remaining := remainingKeyUsagePercent(used)
		window.OfficialRemainingPercent = &remaining
	}
	if view.AccountUnattributedPercent != nil {
		unattributed := clampKeyUsagePercent(*view.AccountUnattributedPercent)
		window.OfficialUnattributedPercent = &unattributed
	}

	limit := clampKeyUsagePercent(view.LimitPercent)
	effectiveLimit := clampKeyUsagePercent(view.EffectiveLimitPercent)
	used := clampKeyUsagePercent(view.AttributedPercent)
	remaining := math.Max(0, effectiveLimit-used)
	poolAvailable := clampKeyUsagePercent(view.PoolAvailablePercent)
	window.UserLimitPercent = &limit
	window.UserEffectiveLimitPercent = &effectiveLimit
	window.UserUsedPercent = &used
	window.UserRemainingPercent = &remaining
	window.UserPoolAvailablePercent = &poolAvailable
	window.UserResetAt = view.WindowResetAt
	window.UserForceUnattributed = view.ForceUnattributed
	window.UserSharedPoolMode = view.SharedPoolMode
	if view.SharedPoolMode {
		// Personal caps are suspended; report only known shared headroom.
		window.UserEffectiveLimitPercent = nil
		window.UserRemainingPercent = nil
		window.UserPoolAvailablePercent = nil
		if view.AccountUsedPercent != nil {
			headroom := math.Max(0, view.CeilingPercent-*view.AccountUsedPercent)
			window.UserRemainingPercent = &headroom
		}
	}
}

func buildStoredAccountWindow(account *service.Account, windowType string, now time.Time) (*keyUsageAccountWindow, bool) {
	if account == nil {
		return nil, false
	}
	if window, found := buildCodexAccountWindow(account.Extra, windowType, now); found {
		return window, true
	}

	switch windowType {
	case service.WindowType5h:
		return buildSessionAccountWindow(account, now)
	case service.WindowType7d:
		return buildPassiveAccountWindow(account.Extra, now)
	default:
		return nil, false
	}
}

func buildCodexAccountWindow(extra map[string]any, windowType string, now time.Time) (*keyUsageAccountWindow, bool) {
	usedKey, resetKey, resetAfterKey := "", "", ""
	switch windowType {
	case service.WindowType5h:
		usedKey, resetKey, resetAfterKey = "codex_5h_used_percent", "codex_5h_reset_at", "codex_5h_reset_after_seconds"
	case service.WindowType7d:
		usedKey, resetKey, resetAfterKey = "codex_7d_used_percent", "codex_7d_reset_at", "codex_7d_reset_after_seconds"
	default:
		return nil, false
	}
	used, found := keyUsageExtraFloat(extra, usedKey)
	if !found {
		return nil, false
	}

	resetAt, _ := keyUsageExtraTime(extra, resetKey)
	observedAt, _ := keyUsageExtraTime(extra, "codex_usage_updated_at")
	if resetAt == nil {
		if seconds, ok := keyUsageExtraFloat(extra, resetAfterKey); ok && seconds > 0 {
			base := now
			if observedAt != nil {
				base = *observedAt
			}
			candidate := base.Add(time.Duration(seconds) * time.Second)
			resetAt = &candidate
		}
	}
	if resetAt != nil && !resetAt.After(now) {
		used = 0
		resetAt = nil
	}

	used = clampKeyUsagePercent(used)
	remaining := remainingKeyUsagePercent(used)
	return &keyUsageAccountWindow{
		OfficialUsedPercent:      &used,
		OfficialRemainingPercent: &remaining,
		OfficialResetAt:          resetAt,
		OfficialObservedAt:       observedAt,
	}, true
}

func buildSessionAccountWindow(account *service.Account, now time.Time) (*keyUsageAccountWindow, bool) {
	if account == nil || account.SessionWindowEnd == nil {
		return nil, false
	}

	used, found := keyUsageExtraFloat(account.Extra, "session_window_utilization")
	if found {
		used *= 100
	} else {
		switch account.SessionWindowStatus {
		case "rejected":
			used, found = 100, true
		case "allowed_warning":
			used, found = 80, true
		}
	}
	if !found {
		used = 0
	}
	resetAt := account.SessionWindowEnd
	if !resetAt.After(now) {
		used = 0
		resetAt = nil
	}
	used = clampKeyUsagePercent(used)
	remaining := remainingKeyUsagePercent(used)
	return &keyUsageAccountWindow{
		OfficialUsedPercent:      &used,
		OfficialRemainingPercent: &remaining,
		OfficialResetAt:          resetAt,
	}, true
}

func buildPassiveAccountWindow(extra map[string]any, now time.Time) (*keyUsageAccountWindow, bool) {
	utilization, hasUtilization := keyUsageExtraFloat(extra, "passive_usage_7d_utilization")
	resetAt, hasResetAt := keyUsageUnixTime(extra, "passive_usage_7d_reset")
	if !hasUtilization && !hasResetAt {
		return nil, false
	}
	used := clampKeyUsagePercent(utilization * 100)
	if resetAt != nil && !resetAt.After(now) {
		used = 0
		resetAt = nil
	}
	remaining := remainingKeyUsagePercent(used)
	observedAt, _ := keyUsageExtraTime(extra, "passive_usage_sampled_at")
	return &keyUsageAccountWindow{
		OfficialUsedPercent:      &used,
		OfficialRemainingPercent: &remaining,
		OfficialResetAt:          resetAt,
		OfficialObservedAt:       observedAt,
	}, true
}

func buildLocalAccountQuota(account *service.Account, now time.Time) *keyUsageLocalQuota {
	if account == nil {
		return nil
	}
	quota := &keyUsageLocalQuota{
		Total: buildLocalQuotaDimension(account.GetQuotaLimit(), account.GetQuotaUsed(), keyUsageQuotaResetAt(account, "total", now)),
	}

	dailyUsed := account.GetQuotaDailyUsed()
	dailyResetAt := keyUsageQuotaResetAt(account, "daily", now)
	if dailyResetAt == nil {
		dailyUsed = 0
	}
	quota.Daily = buildLocalQuotaDimension(account.GetQuotaDailyLimit(), dailyUsed, dailyResetAt)

	weeklyUsed := account.GetQuotaWeeklyUsed()
	weeklyResetAt := keyUsageQuotaResetAt(account, "weekly", now)
	if weeklyResetAt == nil {
		weeklyUsed = 0
	}
	quota.Weekly = buildLocalQuotaDimension(account.GetQuotaWeeklyLimit(), weeklyUsed, weeklyResetAt)

	if quota.Total == nil && quota.Daily == nil && quota.Weekly == nil {
		return nil
	}
	return quota
}

func buildLocalQuotaDimension(limit, used float64, resetAt *time.Time) *keyUsageQuotaDimension {
	if limit <= 0 || math.IsNaN(limit) || math.IsInf(limit, 0) {
		return nil
	}
	if used < 0 || math.IsNaN(used) || math.IsInf(used, 0) {
		used = 0
	}
	remaining := math.Max(0, limit-used)
	return &keyUsageQuotaDimension{Limit: limit, Used: used, Remaining: remaining, ResetAt: resetAt, Unit: "USD"}
}

func keyUsageQuotaResetAt(account *service.Account, dimension string, now time.Time) *time.Time {
	if account == nil {
		return nil
	}
	resetKey, startKey, duration := "", "", time.Duration(0)
	switch dimension {
	case "daily":
		resetKey, startKey, duration = "quota_daily_reset_at", "quota_daily_start", 24*time.Hour
	case "weekly":
		resetKey, startKey, duration = "quota_weekly_reset_at", "quota_weekly_start", 7*24*time.Hour
	default:
		return nil
	}
	if resetAt, ok := keyUsageExtraTime(account.Extra, resetKey); ok && resetAt.After(now) {
		return resetAt
	}
	if startAt, ok := keyUsageExtraTime(account.Extra, startKey); ok {
		resetAt := startAt.Add(duration)
		if resetAt.After(now) {
			return &resetAt
		}
	}
	return nil
}

func keyUsageAccountEmail(account *service.Account) string {
	if account == nil {
		return ""
	}
	for _, key := range []string{"email_address", "email"} {
		if value, ok := account.Extra[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return strings.TrimSpace(account.GetCredential("email"))
}

func keyUsageExtraFloat(extra map[string]any, key string) (float64, bool) {
	if extra == nil {
		return 0, false
	}
	raw, ok := extra[key]
	if !ok || raw == nil {
		return 0, false
	}
	switch value := raw.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case json.Number:
		parsed, err := value.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func keyUsageExtraTime(extra map[string]any, key string) (*time.Time, bool) {
	if extra == nil {
		return nil, false
	}
	raw, ok := extra[key]
	if !ok || raw == nil {
		return nil, false
	}
	switch value := raw.(type) {
	case time.Time:
		copy := value
		return &copy, true
	case *time.Time:
		if value == nil {
			return nil, false
		}
		copy := *value
		return &copy, true
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
		if err != nil {
			parsed, err = time.Parse(time.RFC3339, strings.TrimSpace(value))
		}
		if err == nil {
			return &parsed, true
		}
		if unix, parseErr := strconv.ParseInt(strings.TrimSpace(value), 10, 64); parseErr == nil {
			parsed = time.Unix(unix, 0)
			return &parsed, true
		}
	case json.Number:
		if unix, err := strconv.ParseInt(value.String(), 10, 64); err == nil {
			parsed := time.Unix(unix, 0)
			return &parsed, true
		}
	default:
		if unix, ok := keyUsageExtraFloat(extra, key); ok {
			parsed := time.Unix(int64(unix), 0)
			return &parsed, true
		}
	}
	return nil, false
}

func keyUsageUnixTime(extra map[string]any, key string) (*time.Time, bool) {
	return keyUsageExtraTime(extra, key)
}

func clampKeyUsagePercent(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 0
	}
	if value >= 100 {
		return 100
	}
	return value
}

func remainingKeyUsagePercent(used float64) float64 {
	return math.Max(0, 100-clampKeyUsagePercent(used))
}
