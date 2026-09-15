package handler

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type limitedKeyCache struct {
	helperConcurrencyCacheStub
	allowed bool
	err     error
	limit   int
}

func (s *limitedKeyCache) AcquireAPIKeySlot(_ context.Context, _ int64, limit int, _ string) (bool, error) {
	s.limit = limit
	return s.allowed, s.err
}

func TestAPIKeyConcurrencyRejectionReleasesUserSlot(t *testing.T) {
	for _, cacheErr := range []error{nil, errors.New("redis unavailable")} {
		cache := &limitedKeyCache{helperConcurrencyCacheStub: helperConcurrencyCacheStub{userSeq: []bool{true}}, err: cacheErr}
		helper := NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second)
		c, _ := newHelperTestContext(http.MethodPost, "/v1/responses")
		c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 77, MaxConcurrency: 2})
		streamStarted := false
		release, err := helper.AcquireUserSlotWithWait(c, 202, 5, false, &streamStarted)
		require.Nil(t, release)
		require.Error(t, err)
		require.Equal(t, 2, cache.limit)
		require.Equal(t, 1, cache.userReleaseCalls)
		require.Zero(t, cache.apiKeyTrackCalls)
		status, _, _, message := concurrencyErrorResponse(err, "user")
		if cacheErr == nil {
			require.Equal(t, http.StatusTooManyRequests, status)
			require.Contains(t, message, "api_key")
		} else {
			require.Equal(t, http.StatusServiceUnavailable, status)
		}
	}
}

func TestAPIKeyConcurrencyWebsocketTurnCancellation(t *testing.T) {
	cache := &limitedKeyCache{helperConcurrencyCacheStub: helperConcurrencyCacheStub{userSeq: []bool{true}}, allowed: true}
	helper := NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release, acquired, err := helper.TryAcquireUserSlotForAPIKey(ctx, 202, 5, 77, 2)
	require.NoError(t, err)
	require.True(t, acquired)
	release = wrapReleaseOnDone(ctx, release)
	cancel()
	require.Eventually(t, func() bool {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		return cache.userReleaseCalls == 1 && cache.apiKeyReleaseCalls == 1
	}, time.Second, time.Millisecond)
	release()
	release()
	require.Equal(t, 1, cache.userReleaseCalls)
	require.Equal(t, 1, cache.apiKeyReleaseCalls)
}
