package service

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"testing"
)

func (r *sharedPoolWindowRepo) SetAccountCreditSpending(_ context.Context, accountID int64, enabled bool) error {
	for i := range r.rows {
		if r.rows[i].AccountID == accountID {
			r.rows[i].CreditSpendingEnabled = enabled
		}
	}
	return nil
}
func (r *sharedPoolWindowRepo) ListCreditSpendingAccountIDs(context.Context) ([]int64, error) {
	ids := []int64{}
	for _, row := range r.rows {
		if row.CreditSpendingEnabled {
			ids = append(ids, row.AccountID)
			break
		}
	}
	return ids, nil
}

func TestAccountWindowCreditSpendingPermissionPrecedesSharedPool(t *testing.T) {
	for _, window := range accountWindowTypes {
		t.Run(window, func(t *testing.T) {
			used := 100.0
			repo := &sharedPoolWindowRepo{stubWindowRepo: stubWindowRepo{rows: []UserAccountWindowQuotaRecord{
				awqRec(1, WindowType5h, 23, 30, 0), awqRec(1, WindowType7d, 23, 30, 0),
			}}}
			if window == WindowType5h {
				repo.official5h = &used
			} else {
				repo.official7d = &used
			}
			svc := NewAccountWindowQuotaService(repo, nil)
			ctx := context.Background()
			require.NoError(t, svc.SetAccountSharedPoolMode(ctx, 1, true))
			original := append([]UserAccountWindowQuotaRecord(nil), repo.rows...)
			eligible, blocked, _ := svc.CheckUserAccountEligible(ctx, 1, 1)
			require.False(t, eligible, "shared pool alone cannot spend credits")
			require.Equal(t, window, blocked)
			require.NoError(t, svc.SetAccountCreditSpending(ctx, 1, true))
			svc = NewAccountWindowQuotaService(repo, nil)
			eligible, _, _ = svc.CheckUserAccountEligible(ctx, 1, 1)
			require.True(t, eligible, "explicit permission permits continuation at official exhaustion")
			eligible, _, _ = svc.CheckUserAccountEligible(ctx, 999, 1)
			require.False(t, eligible, "permission never creates membership")
			require.NoError(t, svc.SetAccountSharedPoolMode(ctx, 1, false))
			eligible, _, _ = svc.CheckUserAccountEligible(ctx, 1, 1)
			require.False(t, eligible, "credit permission does not erase exhausted personal caps")
			require.NoError(t, svc.SetAccountSharedPoolMode(ctx, 1, true))
			require.NoError(t, svc.SetAccountCreditSpending(ctx, 1, false))
			eligible, _, _ = svc.CheckUserAccountEligible(ctx, 1, 1)
			require.False(t, eligible, "revocation takes effect on the next check even with shared pool still on")
			require.Equal(t, original, repo.rows, "permission toggles preserve limits, donations and usage")
		})
	}
}

func TestAccountWindowCreditSpendingDoesNotChangeOtherAccount(t *testing.T) {
	used := 100.0
	repo := &sharedPoolWindowRepo{stubWindowRepo: stubWindowRepo{official7d: &used, rows: []UserAccountWindowQuotaRecord{
		{UserID: 1, AccountID: 1, WindowType: WindowType7d, LimitPercent: 23, SharedPoolMode: true},
		{UserID: 1, AccountID: 2, WindowType: WindowType7d, LimitPercent: 23, SharedPoolMode: true},
	}}}
	svc := NewAccountWindowQuotaService(repo, nil)
	require.NoError(t, svc.SetAccountCreditSpending(context.Background(), 1, true))
	eligible, _, _ := svc.CheckUserAccountEligible(context.Background(), 1, 2)
	require.False(t, eligible)
}

func TestAccountWindowCreditSpendingIsNotInheritedByDuplicates(t *testing.T) {
	source := map[string]any{AccountWindowCreditSpendingExtraKey: true, "note": "keep"}
	extra, err := duplicateAccountExtra(source)
	require.NoError(t, err)
	require.NotContains(t, extra, AccountWindowCreditSpendingExtraKey)
	require.Equal(t, "keep", extra["note"])
	require.Equal(t, true, source[AccountWindowCreditSpendingExtraKey])
}

type creditPermissionReadErrorRepo struct{ stubWindowRepo }

func (r *creditPermissionReadErrorRepo) ListByAccount(context.Context, int64) ([]UserAccountWindowQuotaRecord, error) {
	return nil, errors.New("unavailable")
}

type creditOfficialReadErrorRepo struct{ stubWindowRepo }

func (r *creditOfficialReadErrorRepo) GetAccountOfficialWindowPercent(context.Context, int64, string) (float64, bool, error) {
	return 0, false, errors.New("unavailable")
}
func TestAccountWindowCreditSpendingFailsClosedWhenQuotaCannotBeVerified(t *testing.T) {
	records := []UserAccountWindowQuotaRecord{{UserID: 1, AccountID: 1, WindowType: WindowType7d, LimitPercent: 23, SharedPoolMode: true}}
	for _, repo := range []UserAccountWindowQuotaRepository{
		&stubWindowRepo{rows: records},
		&creditPermissionReadErrorRepo{},
		&creditOfficialReadErrorRepo{stubWindowRepo{rows: records}},
	} {
		eligible, window, _ := NewAccountWindowQuotaService(repo, nil).CheckUserAccountEligible(context.Background(), 1, 1)
		require.False(t, eligible)
		require.Equal(t, AccountWindowQuotaUnavailable, window)
	}
	zero := 0.0
	svc := NewAccountWindowQuotaService(&stubWindowRepo{rows: records, official7d: &zero}, nil)
	eligible, _, _ := svc.CheckUserAccountEligible(context.Background(), 1, 1)
	require.True(t, eligible, "a valid weekly zero is sufficient; Pro need not expose a 5h window")
}
