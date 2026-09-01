package handler

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestBuildKeyUsageAccountUsesDisplaySafeFields(t *testing.T) {
	// The account quota model evaluates rolling windows against time.Now(). Keep
	// the fixture inside its active window so the assertion is independent of
	// the date on which the test is run.
	now := time.Now().UTC().Truncate(time.Second)
	resetAt := now.Add(3 * time.Hour)
	account := &service.Account{
		ID:       51,
		Name:     "暄",
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
		Status:   service.StatusActive,
		Credentials: map[string]any{
			"email":        "1322942653@qq.com",
			"access_token": "must-not-leak",
		},
		Extra: map[string]any{
			"codex_7d_used_percent":  42.5,
			"codex_7d_reset_at":      resetAt.Format(time.RFC3339),
			"codex_usage_updated_at": now.Add(-time.Hour).Format(time.RFC3339),
			"quota_weekly_limit":     100.0,
			"quota_weekly_used":      35.0,
			"quota_weekly_start":     now.Add(-24 * time.Hour).Format(time.RFC3339),
		},
	}

	got := buildKeyUsageAccount(account, nil, now)
	payload, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(payload), "must-not-leak")
	require.Equal(t, int64(51), got.ID)
	require.Equal(t, "1322942653@qq.com", got.Email)
	require.NotNil(t, got.Windows)
	require.InDelta(t, 42.5, *got.Windows[service.WindowType7d].OfficialUsedPercent, 0.001)
	require.InDelta(t, 57.5, *got.Windows[service.WindowType7d].OfficialRemainingPercent, 0.001)
	require.Equal(t, resetAt, *got.Windows[service.WindowType7d].OfficialResetAt)
	require.NotNil(t, got.LocalQuota)
	require.InDelta(t, 65.0, got.LocalQuota.Weekly.Remaining, 0.001)
}

func TestBuildKeyUsageAccountAddsCurrentUserWindowQuota(t *testing.T) {
	now := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	officialUsed := 48.0
	account := &service.Account{
		ID:       51,
		Name:     "shared-openai",
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
		Status:   service.StatusActive,
		Extra: map[string]any{
			service.AccountExtraWindowQuotaShared: true,
		},
	}

	got := buildKeyUsageAccount(account, map[string]service.UserWindowQuotaView{
		service.WindowType7d: {
			LimitPercent:               23,
			EffectiveLimitPercent:      28,
			AttributedPercent:          9,
			AccountUsedPercent:         &officialUsed,
			WindowResetAt:              ptrTime(now.Add(4 * 24 * time.Hour)),
			PoolAvailablePercent:       6,
			AccountUnattributedPercent: ptrFloat(39),
		},
	}, now)

	window, ok := got.Windows[service.WindowType7d]
	require.True(t, ok)
	require.True(t, got.Shared)
	require.InDelta(t, 48, *window.OfficialUsedPercent, 0.001)
	require.InDelta(t, 23, *window.UserLimitPercent, 0.001)
	require.InDelta(t, 28, *window.UserEffectiveLimitPercent, 0.001)
	require.InDelta(t, 9, *window.UserUsedPercent, 0.001)
	require.InDelta(t, 19, *window.UserRemainingPercent, 0.001)
	require.InDelta(t, 6, *window.UserPoolAvailablePercent, 0.001)
	require.InDelta(t, 39, *window.OfficialUnattributedPercent, 0.001)
}

func ptrFloat(value float64) *float64 {
	return &value
}

func ptrTime(value time.Time) *time.Time {
	return &value
}
