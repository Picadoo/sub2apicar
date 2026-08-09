package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type accountWindowQuotaHandlerRepo struct {
	rows             []service.UserAccountWindowQuotaRecord
	adminRows        []service.AdminWindowQuotaOverviewRow
	maxConfigured    float64
	equalizeResults  []service.AccountWindowEqualizationResult
	equalizeErr      error
	sharedAccountIDs []int64
	official5h       *float64
	official7d       *float64
}

func (r *accountWindowQuotaHandlerRepo) ResetWindowForAccountIfDue(context.Context, int64, string, time.Time) (bool, error) {
	return true, nil
}
func (r *accountWindowQuotaHandlerRepo) GetByUserAccountWindow(context.Context, int64, int64, string) (*service.UserAccountWindowQuotaRecord, error) {
	return nil, nil
}
func (r *accountWindowQuotaHandlerRepo) ListByUser(context.Context, int64) ([]service.UserAccountWindowQuotaRecord, error) {
	return r.rows, nil
}
func (r *accountWindowQuotaHandlerRepo) ListByAccount(context.Context, int64) ([]service.UserAccountWindowQuotaRecord, error) {
	return r.rows, nil
}
func (r *accountWindowQuotaHandlerRepo) ListDueResets(context.Context, time.Time) ([]service.AccountWindowReset, error) {
	return nil, nil
}
func (r *accountWindowQuotaHandlerRepo) SetLimitForUserAccount(context.Context, int64, int64, string, float64) error {
	return nil
}
func (r *accountWindowQuotaHandlerRepo) SetDonatePoolFraction(context.Context, int64, int64, string, float64, float64) error {
	return nil
}
func (r *accountWindowQuotaHandlerRepo) ApplyWindowSharesFromCheckpoint(_ context.Context, _ int64, _ string, candidate service.AccountWindowAttributionCheckpoint, _ *time.Time) (service.AccountWindowAttributionCheckpoint, bool, error) {
	return candidate, true, nil
}
func (r *accountWindowQuotaHandlerRepo) ListAllWithUser(context.Context) ([]service.AdminWindowQuotaOverviewRow, error) {
	return r.adminRows, nil
}
func (r *accountWindowQuotaHandlerRepo) GetMaxConfiguredSumForWindow(context.Context, string) (float64, error) {
	return r.maxConfigured, nil
}
func (r *accountWindowQuotaHandlerRepo) EqualizeActiveMemberLimits(context.Context, int64, float64, float64) ([]service.AccountWindowEqualizationResult, error) {
	return r.equalizeResults, r.equalizeErr
}
func (r *accountWindowQuotaHandlerRepo) SyncAccountMembers(context.Context, int64, []int64, float64, float64) ([]service.AccountWindowEqualizationResult, error) {
	return r.equalizeResults, r.equalizeErr
}
func (r *accountWindowQuotaHandlerRepo) ListSharedAccountIDs(context.Context) ([]int64, error) {
	return append([]int64(nil), r.sharedAccountIDs...), nil
}
func (r *accountWindowQuotaHandlerRepo) GetAccountOfficialWindowPercent(_ context.Context, _ int64, window string) (float64, bool, error) {
	value := r.official5h
	if window == service.WindowType7d {
		value = r.official7d
	}
	if value == nil {
		return 0, false, nil
	}
	return *value, true, nil
}

func newAccountWindowQuotaHandlerTest(t *testing.T, repo service.UserAccountWindowQuotaRepository) (*AccountWindowQuotaHandler, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewAccountWindowQuotaHandler(service.NewAccountWindowQuotaService(repo, rdb)), mr
}

func performAccountWindowQuotaRequest(t *testing.T, method, path, body string, handler gin.HandlerFunc, params ...gin.Param) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	handler(c)
	return w
}

func decodeAccountWindowQuotaResponse(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	data, ok := envelope["data"].(map[string]any)
	require.True(t, ok, "response data must be an object: %s", w.Body.String())
	return data
}

func TestAccountWindowQuotaHandler_AdminOverviewIncludesSummaries(t *testing.T) {
	official5h := 21.5
	repo := &accountWindowQuotaHandlerRepo{
		adminRows: []service.AdminWindowQuotaOverviewRow{
			{UserID: 1, Email: "a@example.com", AccountID: 7, WindowType: service.WindowType5h, LimitPercent: 60, AttributedPercent: 15},
			{UserID: 2, Email: "b@example.com", AccountID: 7, WindowType: service.WindowType5h, LimitPercent: 40, AttributedPercent: 10},
		},
		sharedAccountIDs: []int64{7, 9},
		official5h:       &official5h,
	}
	h, mr := newAccountWindowQuotaHandlerTest(t, repo)
	mr.Set("uawq:ceiling:5h", "92")

	w := performAccountWindowQuotaRequest(t, http.MethodGet, "/api/v1/admin/account-window-quotas/overview", "", h.AdminOverview)
	require.Equal(t, http.StatusOK, w.Code)
	data := decodeAccountWindowQuotaResponse(t, w)
	summaries := data["summaries"].([]any)
	require.Len(t, summaries, 1)
	summary := summaries[0].(map[string]any)
	require.Equal(t, float64(7), summary["account_id"])
	require.Equal(t, service.WindowType5h, summary["window_type"])
	require.Equal(t, float64(2), summary["member_count"])
	require.Equal(t, float64(100), summary["configured_sum_percent"])
	require.Equal(t, official5h, summary["used_sum_percent"])
	require.Equal(t, float64(92), summary["ceiling_percent"])
	require.Equal(t, true, summary["overallocated"])
	accountIDs := data["account_ids"].([]any)
	require.Equal(t, []any{float64(7), float64(9)}, accountIDs)
}

func TestAccountWindowQuotaHandler_GetMyWindowsIncludesOnlySharedAccountMembers(t *testing.T) {
	repo := &accountWindowQuotaHandlerRepo{
		rows: []service.UserAccountWindowQuotaRecord{
			{UserID: 1, AccountID: 7, WindowType: service.WindowType5h, LimitPercent: 46, AttributedPercent: 5},
		},
		adminRows: []service.AdminWindowQuotaOverviewRow{
			{UserID: 1, Email: "me@example.com", AccountID: 7, WindowType: service.WindowType5h, LimitPercent: 46, AttributedPercent: 5},
			{UserID: 2, Email: "mate@example.com", AccountID: 7, WindowType: service.WindowType5h, LimitPercent: 46, AttributedPercent: 8},
			{UserID: 3, Email: "other@example.com", AccountID: 8, WindowType: service.WindowType5h, LimitPercent: 46, AttributedPercent: 9},
		},
	}
	h, _ := newAccountWindowQuotaHandlerTest(t, repo)
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/user/account-window-quotas", nil)
	c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 1})

	h.GetMyWindows(c)

	require.Equal(t, http.StatusOK, w.Code)
	data := decodeAccountWindowQuotaResponse(t, w)
	windows := data["windows"].([]any)
	require.Len(t, windows, 1)
	_, hasAccountUsed := windows[0].(map[string]any)["account_used_percent"]
	require.False(t, hasAccountUsed, "无官方快照时不得用成员归因之和伪造账号总量")
	members := data["members"].([]any)
	require.Len(t, members, 2)
	for _, raw := range members {
		require.Equal(t, float64(7), raw.(map[string]any)["account_id"])
	}
}

func TestAccountWindowQuotaHandler_AdminGetUserWindowsIncludesPoolAndOfficialBreakdown(t *testing.T) {
	official5h := 10.0
	resetAt := time.Now().Add(time.Hour)
	repo := &accountWindowQuotaHandlerRepo{
		rows: []service.UserAccountWindowQuotaRecord{
			{
				UserID:             1,
				AccountID:          7,
				WindowType:         service.WindowType5h,
				LimitPercent:       23,
				AttributedPercent:  0,
				DonatePoolFraction: 1,
				WindowResetAt:      &resetAt,
			},
		},
		official5h: &official5h,
	}
	h, _ := newAccountWindowQuotaHandlerTest(t, repo)

	w := performAccountWindowQuotaRequest(t, http.MethodGet, "/api/v1/admin/account-window-quotas/users/1", "", h.AdminGetUserWindows, gin.Param{Key: "id", Value: "1"})
	require.Equal(t, http.StatusOK, w.Code)
	data := decodeAccountWindowQuotaResponse(t, w)
	windows := data["windows"].([]any)
	require.Len(t, windows, 1)
	window := windows[0].(map[string]any)
	require.Equal(t, float64(0), window["effective_limit_percent"])
	require.Equal(t, float64(0), window["remaining_percent"])
	require.Equal(t, official5h, window["account_used_percent"])
	require.Equal(t, float64(0), window["account_attributed_percent"])
	require.Equal(t, official5h, window["account_unattributed_percent"])
}

func TestAccountWindowQuotaHandler_AdminEqualizeAccountLimits(t *testing.T) {
	repo := &accountWindowQuotaHandlerRepo{equalizeResults: []service.AccountWindowEqualizationResult{
		{WindowType: service.WindowType5h, ActiveMemberCount: 2, SharePercent: 46, CeilingPercent: 92},
		{WindowType: service.WindowType7d, ActiveMemberCount: 4, SharePercent: 23, CeilingPercent: 92},
	}}
	h, _ := newAccountWindowQuotaHandlerTest(t, repo)

	w := performAccountWindowQuotaRequest(t, http.MethodPost, "/api/v1/admin/account-window-quotas/accounts/7/equalize", "", h.AdminEqualizeAccountLimits, gin.Param{Key: "id", Value: "7"})
	require.Equal(t, http.StatusOK, w.Code)
	data := decodeAccountWindowQuotaResponse(t, w)
	require.Equal(t, float64(7), data["account_id"])
	require.Len(t, data["windows"], 2)
}

func TestAccountWindowQuotaHandler_AdminSetAccountMembersIncludesUnusedUsers(t *testing.T) {
	repo := &accountWindowQuotaHandlerRepo{equalizeResults: []service.AccountWindowEqualizationResult{
		{WindowType: service.WindowType5h, ActiveMemberCount: 3, SharePercent: 30.6666, CeilingPercent: 92},
		{WindowType: service.WindowType7d, ActiveMemberCount: 3, SharePercent: 30.6666, CeilingPercent: 92},
	}}
	h, _ := newAccountWindowQuotaHandlerTest(t, repo)

	w := performAccountWindowQuotaRequest(t, http.MethodPut, "/api/v1/admin/account-window-quotas/accounts/7/members", `{"user_ids":[1,2,3]}`, h.AdminSetAccountMembers, gin.Param{Key: "id", Value: "7"})
	require.Equal(t, http.StatusOK, w.Code)
	data := decodeAccountWindowQuotaResponse(t, w)
	require.Equal(t, float64(7), data["account_id"])
	require.Len(t, data["windows"], 2)
}

func TestAccountWindowQuotaHandler_AdminSetCeilingRejectsNewInconsistency(t *testing.T) {
	repo := &accountWindowQuotaHandlerRepo{maxConfigured: 95}
	h, _ := newAccountWindowQuotaHandlerTest(t, repo)

	w := performAccountWindowQuotaRequest(t, http.MethodPost, "/api/v1/admin/account-window-quotas/ceiling", `{"window_type":"5h","ceiling_percent":92}`, h.AdminSetCeiling)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "highest configured sum is 95.00%")
}

func TestBuildWindowItemsIncludesMinimumDonationFraction(t *testing.T) {
	items := buildWindowItemsFromViews([]service.UserWindowQuotaView{{
		AccountID:             7,
		WindowType:            service.WindowType5h,
		LimitPercent:          23,
		DonateFraction:        1,
		MinimumDonateFraction: 12.0 / 23.0,
		EffectiveLimitPercent: 0,
	}}, time.Now())

	require.Len(t, items, 1)
	require.InDelta(t, 12.0/23.0, items[0].MinimumDonateFraction, 1e-9)
}

// 全捐且未用的捐赠者有效上限是合法的 0，不能回落成基础上限展示。
func TestBuildOverviewItemsKeepsZeroEffectiveLimitForFullDonor(t *testing.T) {
	rows := buildOverviewItems([]service.AdminWindowQuotaOverviewRow{{
		UserID:                2,
		AccountID:             7,
		WindowType:            service.WindowType5h,
		LimitPercent:          23,
		AttributedPercent:     0,
		DonatePoolFraction:    1,
		EffectiveLimitPercent: 0,
	}}, time.Now())

	require.Len(t, rows, 1)
	require.Zero(t, rows[0].EffectiveLimitPercent)
	require.Zero(t, rows[0].RemainingPercent)
}

func TestAccountWindowQuotaHandler_DonateRejectsBorrowedReclaim(t *testing.T) {
	repo := &accountWindowQuotaHandlerRepo{rows: []service.UserAccountWindowQuotaRecord{
		{UserID: 1, AccountID: 7, WindowType: service.WindowType5h, LimitPercent: 23, AttributedPercent: 35},
		{UserID: 2, AccountID: 7, WindowType: service.WindowType5h, LimitPercent: 23, DonatePoolFraction: 1},
	}}
	h, _ := newAccountWindowQuotaHandlerTest(t, repo)
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/user/account-window-quotas/donate", strings.NewReader(`{"account_id":7,"window_type":"5h","fraction":0}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 2})

	h.Donate(c)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), service.ErrAccountWindowDonationInUse.Error())
}
