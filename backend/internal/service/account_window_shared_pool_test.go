package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type sharedPoolWindowRepo struct{ stubWindowRepo }

func (r *sharedPoolWindowRepo) SetAccountSharedPoolMode(_ context.Context, accountID int64, enabled bool) error {
	for i := range r.rows {
		if r.rows[i].AccountID == accountID {
			r.rows[i].SharedPoolMode = enabled
		}
	}
	return nil
}

func (r *sharedPoolWindowRepo) ListSharedPoolAccountIDs(context.Context) ([]int64, error) {
	return []int64{1}, nil
}

func (r *sharedPoolWindowRepo) ListByAccount(_ context.Context, accountID int64) ([]UserAccountWindowQuotaRecord, error) {
	var rows []UserAccountWindowQuotaRecord
	for _, row := range r.rows {
		if row.AccountID == accountID {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func TestAccountWindowSharedPoolModePreservesUsageAndRestoresPersonalCaps(t *testing.T) {
	ctx := context.Background()
	used := 60.0
	repo := &sharedPoolWindowRepo{stubWindowRepo{rows: []UserAccountWindowQuotaRecord{
		awqRec(1, WindowType5h, 23, 30, 0.5),
		awqRec(1, WindowType7d, 23, 30, 1),
	}, official5h: &used, official7d: &used}}
	other := awqRec(1, WindowType5h, 23, 30, 0)
	other.AccountID = 2
	repo.rows = append(repo.rows, other)
	original := append([]UserAccountWindowQuotaRecord(nil), repo.rows...)
	svc := NewAccountWindowQuotaService(repo, nil)
	eligible, _, _ := svc.CheckUserAccountEligible(ctx, 1, 1)
	require.False(t, eligible)
	require.NoError(t, svc.SetAccountSharedPoolMode(ctx, 1, true))

	// A fresh service instance sees the persisted account setting as well.
	svc = NewAccountWindowQuotaService(repo, nil)
	eligible, _, _ = svc.CheckUserAccountEligible(ctx, 1, 1)
	require.True(t, eligible, "both exhausted personal windows and donor caps are suspended")
	eligible, _, _ = svc.CheckUserAccountEligible(ctx, 999, 1)
	require.False(t, eligible, "mode must not add members")
	eligible, _, _ = svc.CheckUserAccountEligible(ctx, 1, 2)
	require.False(t, eligible, "other accounts remain restricted")
	views, err := svc.ListUserWindowsWithPool(ctx, 1)
	require.NoError(t, err)
	require.True(t, views[0].SharedPoolMode)
	require.Equal(t, 30.0, views[0].AttributedPercent)

	require.NoError(t, svc.SetAccountSharedPoolMode(ctx, 1, false))
	eligible, window, _ := svc.CheckUserAccountEligible(ctx, 1, 1)
	require.False(t, eligible)
	require.Equal(t, WindowType7d, window)
	require.Equal(t, original, repo.rows, "toggles must preserve limits, usage, donation settings and reset boundaries")
}

func TestAccountWindowSharedPoolModeKeepsOfficialCeilings(t *testing.T) {
	for _, window := range accountWindowTypes {
		t.Run(window, func(t *testing.T) {
			used := 92.0
			repo := &sharedPoolWindowRepo{stubWindowRepo{rows: []UserAccountWindowQuotaRecord{
				awqRec(1, WindowType5h, 23, 0, 0), awqRec(1, WindowType7d, 23, 0, 0),
			}}}
			if window == WindowType5h {
				repo.official5h = &used
			} else {
				repo.official7d = &used
			}
			svc := NewAccountWindowQuotaService(repo, nil)
			require.NoError(t, svc.SetAccountSharedPoolMode(context.Background(), 1, true))
			eligible, blocked, _ := svc.CheckUserAccountEligible(context.Background(), 1, 1)
			require.False(t, eligible)
			require.Equal(t, window, blocked)
		})
	}
}
