package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type openAIQuotaQueryStub struct {
	usage     *service.OpenAIQuotaUsage
	err       error
	accountID int64
}

func (s *openAIQuotaQueryStub) QueryUsage(_ context.Context, accountID int64) (*service.OpenAIQuotaUsage, error) {
	s.accountID = accountID
	return s.usage, s.err
}

func (s *openAIQuotaQueryStub) ResetCredit(_ context.Context, _ int64) (*service.OpenAIQuotaResetResult, error) {
	return nil, errors.New("reset credit is not configured for this test")
}

func (s *openAIQuotaQueryStub) CacheResetCreditsSnapshot(_ context.Context, _ int64, _ *service.OpenAIRateLimitResetCredits) error {
	return nil
}

func (s *openAIQuotaQueryStub) CachePostResetSnapshot(_ context.Context, _ int64, _ *service.OpenAIQuotaUsage) error {
	return nil
}

type accountWindowQuotaSyncStub struct {
	accountID int64
	snapshot  *service.OpenAICodexUsageSnapshot
	err       error
	calls     int
}

func (s *accountWindowQuotaSyncStub) SyncOfficialSnapshot(_ context.Context, accountID int64, snapshot *service.OpenAICodexUsageSnapshot) error {
	s.calls++
	s.accountID = accountID
	s.snapshot = snapshot
	return s.err
}

func TestOpenAIOAuthHandlerQueryQuotaSynchronizesWindowQuota(t *testing.T) {
	gin.SetMode(gin.TestMode)
	quota := &openAIQuotaQueryStub{usage: &service.OpenAIQuotaUsage{RateLimit: &service.OpenAIRateLimit{
		PrimaryWindow:   &service.OpenAIRateLimitWindow{UsedPercent: 42, LimitWindowSeconds: 18000, ResetAfterSeconds: 3600},
		SecondaryWindow: &service.OpenAIRateLimitWindow{UsedPercent: 18, LimitWindowSeconds: 604800, ResetAfterSeconds: 86400},
	}}}
	windowQuota := &accountWindowQuotaSyncStub{}
	handler := &OpenAIOAuthHandler{quotaService: quota, windowQuota: windowQuota}
	router := gin.New()
	router.GET("/api/v1/admin/openai/accounts/:id/quota", handler.QueryQuota)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/openai/accounts/29/quota", nil)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, int64(29), quota.accountID)
	require.Equal(t, 1, windowQuota.calls)
	require.Equal(t, int64(29), windowQuota.accountID)
	require.NotNil(t, windowQuota.snapshot)
	normalized := windowQuota.snapshot.Normalize()
	require.InDelta(t, 42, *normalized.Used5hPercent, 1e-9)
	require.InDelta(t, 18, *normalized.Used7dPercent, 1e-9)

	var body struct {
		Code int `json:"code"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Zero(t, body.Code)
}

func TestOpenAIOAuthHandlerQueryQuotaKeepsNoWindowSnapshotAsQueryOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	quota := &openAIQuotaQueryStub{usage: &service.OpenAIQuotaUsage{PlanType: "plus"}}
	windowQuota := &accountWindowQuotaSyncStub{}
	handler := &OpenAIOAuthHandler{quotaService: quota, windowQuota: windowQuota}
	router := gin.New()
	router.GET("/api/v1/admin/openai/accounts/:id/quota", handler.QueryQuota)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/openai/accounts/29/quota", nil)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Zero(t, windowQuota.calls)
}

func TestOpenAIOAuthHandlerQueryQuotaReturnsOfficialUsageWhenWindowSyncFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	quota := &openAIQuotaQueryStub{usage: &service.OpenAIQuotaUsage{RateLimit: &service.OpenAIRateLimit{
		PrimaryWindow: &service.OpenAIRateLimitWindow{UsedPercent: 42, LimitWindowSeconds: 18000, ResetAfterSeconds: 3600},
	}}}
	windowQuota := &accountWindowQuotaSyncStub{err: errors.New("redis unavailable")}
	handler := &OpenAIOAuthHandler{quotaService: quota, windowQuota: windowQuota}
	router := gin.New()
	router.GET("/api/v1/admin/openai/accounts/:id/quota", handler.QueryQuota)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/openai/accounts/29/quota", nil)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, 1, windowQuota.calls)
	var body struct {
		Code int `json:"code"`
		Data struct {
			RateLimit *service.OpenAIRateLimit `json:"rate_limit"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Zero(t, body.Code)
	require.NotNil(t, body.Data.RateLimit)
	require.NotNil(t, body.Data.RateLimit.PrimaryWindow)
	require.InDelta(t, 42, body.Data.RateLimit.PrimaryWindow.UsedPercent, 1e-9)
}

func TestOpenAIOAuthHandlerQueryQuotaPrefersInternalSnapshotForMixedPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	floatPtr := func(value float64) *float64 { return &value }
	intPtr := func(value int) *int { return &value }

	for _, tt := range []struct {
		name   string
		want5h float64
		want7d float64
	}{
		{name: "ordinary account top-level snapshot", want5h: 91, want7d: 81},
		{name: "spark shadow bengalfox snapshot", want5h: 41, want7d: 11},
	} {
		t.Run(tt.name, func(t *testing.T) {
			usage := &service.OpenAIQuotaUsage{
				RateLimit: &service.OpenAIRateLimit{
					PrimaryWindow:   &service.OpenAIRateLimitWindow{UsedPercent: 91, LimitWindowSeconds: 18000, ResetAfterSeconds: 91},
					SecondaryWindow: &service.OpenAIRateLimitWindow{UsedPercent: 81, LimitWindowSeconds: 604800, ResetAfterSeconds: 81},
				},
				AdditionalRateLimits: []service.OpenAIAdditionalRateLimit{{
					MeteredFeature: "codex_bengalfox",
					RateLimit: &service.OpenAIRateLimit{
						PrimaryWindow:   &service.OpenAIRateLimitWindow{UsedPercent: 41, LimitWindowSeconds: 18000, ResetAfterSeconds: 41},
						SecondaryWindow: &service.OpenAIRateLimitWindow{UsedPercent: 11, LimitWindowSeconds: 604800, ResetAfterSeconds: 11},
					},
				}},
				WindowQuotaSnapshot: &service.OpenAICodexUsageSnapshot{
					PrimaryUsedPercent:         floatPtr(tt.want5h),
					PrimaryResetAfterSeconds:   intPtr(300),
					PrimaryWindowMinutes:       intPtr(300),
					SecondaryUsedPercent:       floatPtr(tt.want7d),
					SecondaryResetAfterSeconds: intPtr(604800),
					SecondaryWindowMinutes:     intPtr(10080),
				},
			}
			quota := &openAIQuotaQueryStub{usage: usage}
			windowQuota := &accountWindowQuotaSyncStub{}
			handler := &OpenAIOAuthHandler{quotaService: quota, windowQuota: windowQuota}
			router := gin.New()
			router.GET("/api/v1/admin/openai/accounts/:id/quota", handler.QueryQuota)

			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/openai/accounts/29/quota", nil)
			router.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, 1, windowQuota.calls)
			require.NotNil(t, windowQuota.snapshot)
			normalized := windowQuota.snapshot.Normalize()
			require.NotNil(t, normalized)
			require.InDelta(t, tt.want5h, *normalized.Used5hPercent, 1e-9)
			require.InDelta(t, tt.want7d, *normalized.Used7dPercent, 1e-9)

			var body struct {
				Data struct {
					RateLimit            *service.OpenAIRateLimit            `json:"rate_limit"`
					AdditionalRateLimits []service.OpenAIAdditionalRateLimit `json:"additional_rate_limits"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
			require.InDelta(t, 91, body.Data.RateLimit.PrimaryWindow.UsedPercent, 1e-9)
			require.Len(t, body.Data.AdditionalRateLimits, 1)
			require.InDelta(t, 41, body.Data.AdditionalRateLimits[0].RateLimit.PrimaryWindow.UsedPercent, 1e-9)
			require.NotContains(t, recorder.Body.String(), "window_quota_snapshot")
		})
	}
}
