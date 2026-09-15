//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAPIKeyConcurrencyValidation(t *testing.T) {
	for _, n := range []int{0, 2, 2147483647} {
		require.NoError(t, validateCreateAPIKeyRequest(CreateAPIKeyRequest{MaxConcurrency: n}))
		require.NoError(t, validateUpdateAPIKeyRequest(UpdateAPIKeyRequest{MaxConcurrency: &n}))
	}
	for _, n := range []int{-1, 2147483648} {
		require.Error(t, validateCreateAPIKeyRequest(CreateAPIKeyRequest{MaxConcurrency: n}))
		require.Error(t, validateUpdateAPIKeyRequest(UpdateAPIKeyRequest{MaxConcurrency: &n}))
	}
}

func TestAPIKeyConcurrencySparseUpdateAndCacheRoundTrip(t *testing.T) {
	for _, limit := range []int{0, 2} {
		svc, repo := newUpdateFieldsAPIKeyService(&APIKey{ID: 1, UserID: 7, Key: "sk-test", Status: StatusActive, MaxConcurrency: 5, QuotaUsed: 30})
		key, err := svc.Update(context.Background(), 1, 7, UpdateAPIKeyRequest{MaxConcurrency: &limit})
		require.NoError(t, err)
		require.Equal(t, limit, key.MaxConcurrency)
		require.Equal(t, []APIKeyUpdateFields{{MaxConcurrency: true}}, repo.updateFields)
		require.Equal(t, float64(30), key.QuotaUsed)
		key.User = &User{ID: 7, Status: StatusActive}
		snapshot := svc.snapshotFromAPIKey(context.Background(), key)
		restored := svc.snapshotToAPIKey(key.Key, snapshot)
		require.Equal(t, limit, restored.MaxConcurrency)
	}
}
