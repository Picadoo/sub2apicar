package service

import (
	"context"
	"testing"
	"time"

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

func (r *sharedPoolWindowRepo) ListAllWithUser(context.Context) ([]AdminWindowQuotaOverviewRow, error) {
	rows := make([]AdminWindowQuotaOverviewRow, 0, len(r.rows))
	for _, row := range r.rows {
		rows = append(rows, AdminWindowQuotaOverviewRow{
			SharedPoolMode:     row.SharedPoolMode,
			UserID:             row.UserID,
			AccountID:          row.AccountID,
			WindowType:         row.WindowType,
			LimitPercent:       row.LimitPercent,
			AttributedPercent:  row.AttributedPercent,
			WindowResetAt:      row.WindowResetAt,
			DonatePoolFraction: row.DonatePoolFraction,
		})
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

func TestAccountWindowSharedPoolAutoDisablesOnDue7dButNot5h(t *testing.T) {
	ctx := context.Background()
	past := time.Now().Add(-2 * time.Minute)
	future := time.Now().Add(2 * time.Hour)
	official := 50.0

	t.Run("5h due keeps shared pool active", func(t *testing.T) {
		r5 := awqRec(1, WindowType5h, 23, 30, 0)
		r5.WindowResetAt = &past
		r5.SharedPoolMode = true
		r7 := awqRec(1, WindowType7d, 23, 30, 0)
		r7.WindowResetAt = &future
		r7.SharedPoolMode = true
		repo := &sharedPoolWindowRepo{stubWindowRepo{
			rows:       []UserAccountWindowQuotaRecord{r5, r7},
			official5h: &official,
			official7d: &official,
		}}
		svc := NewAccountWindowQuotaService(repo, nil)

		eligible, _, _ := svc.CheckUserAccountEligible(ctx, 1, 1)
		require.True(t, eligible, "5h expiry must not disable shared pool while 7d is still active")

		views, err := svc.ListUserWindowsWithPool(ctx, 1)
		require.NoError(t, err)
		require.Len(t, views, 2)
		require.True(t, views[0].SharedPoolMode)
		require.True(t, views[1].SharedPoolMode)

		rows, summaries, err := svc.ListAllForAdmin(ctx)
		require.NoError(t, err)
		require.Len(t, rows, 2)
		require.True(t, rows[0].SharedPoolMode)
		require.True(t, rows[1].SharedPoolMode)
		require.Len(t, summaries, 2)
		require.True(t, summaries[0].SharedPoolMode)
		require.True(t, summaries[1].SharedPoolMode)
	})

	t.Run("7d due immediately disables shared pool while keeping credit spending independent", func(t *testing.T) {
		r5 := awqRec(1, WindowType5h, 23, 30, 0)
		r5.WindowResetAt = &future
		r5.SharedPoolMode = true
		r5.CreditSpendingEnabled = true
		r7 := awqRec(1, WindowType7d, 23, 30, 0)
		r7.WindowResetAt = &past
		r7.SharedPoolMode = true
		r7.CreditSpendingEnabled = true
		highOfficial := 100.0
		repo := &sharedPoolWindowRepo{stubWindowRepo{
			rows:       []UserAccountWindowQuotaRecord{r5, r7},
			official5h: &highOfficial,
			official7d: &highOfficial,
		}}
		svc := NewAccountWindowQuotaService(repo, nil)

		// 7d expired -> shared pool immediately turns off -> 5h personal cap (30 >= 23) blocks the request,
		// while CreditSpendingEnabled remains active (did NOT fail on official 100% ceiling).
		eligible, blocked, _ := svc.CheckUserAccountEligible(ctx, 1, 1)
		require.False(t, eligible)
		require.Equal(t, WindowType5h, blocked)

		// Lowering 5h usage below personal cap proves CreditSpendingEnabled is still active despite 100% official.
		repo.rows[0].AttributedPercent = 10
		eligible, _, _ = svc.CheckUserAccountEligible(ctx, 1, 1)
		require.True(t, eligible, "credit spending permission remains independent when shared pool auto-disables on 7d reset")

		views, err := svc.ListUserWindowsWithPool(ctx, 1)
		require.NoError(t, err)
		require.Len(t, views, 2)
		require.False(t, views[0].SharedPoolMode, "5h view must reflect shared pool auto-disabled on 7d reset")
		require.False(t, views[1].SharedPoolMode, "7d view must reflect shared pool auto-disabled on 7d reset")

		rows, summaries, err := svc.ListAllForAdmin(ctx)
		require.NoError(t, err)
		require.Len(t, rows, 2)
		require.False(t, rows[0].SharedPoolMode)
		require.False(t, rows[1].SharedPoolMode)
		require.Len(t, summaries, 2)
		require.False(t, summaries[0].SharedPoolMode)
		require.False(t, summaries[1].SharedPoolMode)
	})
}
