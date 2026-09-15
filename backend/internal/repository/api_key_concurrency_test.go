package repository

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyConcurrencyAtomicLimitAndRelease(t *testing.T) {
	r := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: r.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewConcurrencyCache(client, 15, 900).(*concurrencyCache)
	ctx := context.Background()
	var acquired atomic.Int64
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := cache.AcquireAPIKeySlot(ctx, 1, 2, fmt.Sprintf("r-%d", i))
			if err != nil {
				t.Error(err)
				return
			}
			if ok {
				acquired.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 2, acquired.Load())
	counts, err := cache.GetAPIKeyConcurrencyBatch(ctx, []int64{1})
	require.NoError(t, err)
	require.Equal(t, 2, counts[1])
	// Another key has its own capacity.
	ok, err := cache.AcquireAPIKeySlot(ctx, 2, 2, "other")
	require.NoError(t, err)
	require.True(t, ok)
	members := client.ZRange(ctx, apiKeySlotKey(1), 0, -1).Val()
	require.NoError(t, cache.ReleaseAPIKeySlot(ctx, 1, members[0]))
	ok, err = cache.AcquireAPIKeySlot(ctx, 1, 2, "replacement")
	require.NoError(t, err)
	require.True(t, ok)
	// Orphaned slots expire using the same policy as user/account slots.
	r.SetTime(time.Now().Add(time.Hour))
	ok, err = cache.AcquireAPIKeySlot(ctx, 1, 2, "after-expiry")
	require.NoError(t, err)
	require.True(t, ok)
}

func TestAPIKeyConcurrencyLiveSharesLimit(t *testing.T) {
	r := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: r.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewConcurrencyCache(client, 15, 900).(*concurrencyCache)
	ctx := context.Background()
	ok, err := cache.AcquireAPIKeySlot(ctx, 30, 1, "regular")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = cache.AcquireLiveLease(ctx, 10, 5, 20, 5, 30, 1, "blocked-live", false)
	require.NoError(t, err)
	require.False(t, ok)
	// A Live session takes over its own regular slot without losing capacity.
	ok, err = cache.AcquireLiveLease(ctx, 10, 5, 20, 5, 30, 1, "live", true)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, cache.ReleaseAPIKeySlot(ctx, 30, "regular"))
	ok, err = cache.AcquireAPIKeySlot(ctx, 30, 1, "blocked-http")
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, cache.ReleaseLiveLease(ctx, 10, 20, 30, "live"))
	ok, err = cache.AcquireAPIKeySlot(ctx, 30, 1, "http")
	require.NoError(t, err)
	require.True(t, ok)
}

func TestAPIKeyConcurrencyUnlimitedStillTracks(t *testing.T) {
	r := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: r.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewConcurrencyCache(client, 15, 900)
	svc := service.NewConcurrencyService(cache)
	ctx := context.Background()
	first, err := svc.AcquireAPIKeySlot(ctx, 1, 1)
	require.NoError(t, err)
	defer first.ReleaseFunc()
	second, err := svc.AcquireAPIKeySlot(ctx, 1, 0)
	require.NoError(t, err)
	require.True(t, second.Acquired)
	second.ReleaseFunc()
	first.ReleaseFunc()
	first.ReleaseFunc()
	counts, err := svc.GetAPIKeyConcurrencyBatch(ctx, []int64{1})
	require.NoError(t, err)
	require.Zero(t, counts[1])
}

func TestAPIKeyConcurrencyPersistenceAndAuthProjection(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "key-limit@test.com")
	key := &service.APIKey{UserID: user.ID, Key: "sk-limit-test", Name: "limited", Status: service.StatusActive, MaxConcurrency: 2, QuotaUsed: 7}
	require.NoError(t, repo.Create(ctx, key))
	loaded, err := repo.GetByKeyForAuth(ctx, key.Key)
	require.NoError(t, err)
	require.Equal(t, 2, loaded.MaxConcurrency)
	// Updating this setting must not overwrite concurrently accumulated usage.
	key.MaxConcurrency = 0
	key.QuotaUsed = 0
	require.NoError(t, repo.Update(ctx, key, service.APIKeyUpdateFields{MaxConcurrency: true}))
	loaded, err = repo.GetByID(ctx, key.ID)
	require.NoError(t, err)
	require.Zero(t, loaded.MaxConcurrency)
	require.Equal(t, float64(7), loaded.QuotaUsed)
}
