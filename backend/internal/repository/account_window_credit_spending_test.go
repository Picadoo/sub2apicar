package repository

import (
	"context"
	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAccountWindowCreditSpendingRepositoryUpdatesOnlyPermission(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		repo, mock := newWindowQuotaRepoSQLMock(t)
		mock.ExpectExec(`UPDATE accounts[\s\S]*jsonb_build_object[\s\S]*parent_account_id IS NULL[\s\S]*window_quota_shared`).
			WithArgs(int64(7), service.AccountWindowCreditSpendingExtraKey, enabled).WillReturnResult(sqlmock.NewResult(0, 1))
		require.NoError(t, repo.SetAccountCreditSpending(context.Background(), 7, enabled))
		require.NoError(t, mock.ExpectationsWereMet())
	}
	repo, mock := newWindowQuotaRepoSQLMock(t)
	mock.ExpectExec(`UPDATE accounts[\s\S]*parent_account_id IS NULL`).
		WithArgs(int64(7), service.AccountWindowCreditSpendingExtraKey, true).WillReturnResult(sqlmock.NewResult(0, 0))
	require.ErrorIs(t, repo.SetAccountCreditSpending(context.Background(), 7, true), service.ErrAccountWindowInvalidMembers)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountWindowCreditSpendingRepositoryReadsExplicitPermission(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	mock.ExpectQuery(`SELECT[\s\S]*window_quota_credit_spending_enabled[\s\S]*WHERE q.account_id`).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"user_id", "account_id", "window_type", "limit_percent", "attributed_percent", "window_reset_at", "donate_pool_fraction", "shared_pool_mode", "credit_spending_enabled"}).
		AddRow(1, 7, "7d", 23, 30, nil, 0, true, true))
	rows, err := repo.ListByAccount(context.Background(), 7)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, rows[0].CreditSpendingEnabled)
	mock.ExpectQuery(`SELECT id FROM accounts[\s\S]*parent_account_id IS NULL[\s\S]*false.*jsonb`).
		WithArgs(service.AccountWindowCreditSpendingExtraKey).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
	ids, err := repo.ListCreditSpendingAccountIDs(context.Background())
	require.NoError(t, err)
	require.Equal(t, []int64{7}, ids)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountWindowCreditSpendingSurvivesStaleAccountEdit(t *testing.T) {
	for _, stored := range []string{"", "false", "true"} {
		t.Run(stored, func(t *testing.T) {
			repo, mock := newWindowQuotaRepoSQLMock(t)
			var value any
			if stored != "" {
				value = []byte(stored)
			}
			mock.ExpectQuery(`SELECT[\s\S]*window_quota_credit_spending_enabled[\s\S]*FOR NO KEY UPDATE`).
				WithArgs(int64(7), service.PlatformOpenAI, service.AccountTypeOAuth, `{}`, nil).
				WillReturnRows(sqlmock.NewRows([]string{"identity", "ollama_identity", "proxy_identity", "probe", "rate_sync", "snapshot", "ollama_session", "ollama_auto", "ollama_snapshot", "opencode_identity", "opencode_auto", "opencode_snapshot", "shared_pool_mode", "grok_observed_models", "upstream_model_metadata", "credit_spending_enabled"}).
					AddRow(true, false, true, nil, nil, nil, nil, nil, nil, false, nil, nil, nil, nil, nil, value))
			account := &service.Account{ID: 7, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Extra: map[string]any{service.AccountWindowCreditSpendingExtraKey: stored != "true", "unrelated": "keep"}}
			merged, err := lockAndMergeAccountProbeExtra(context.Background(), repo.client, account, nil, nil)
			require.NoError(t, err)
			if stored == "" {
				require.NotContains(t, merged, service.AccountWindowCreditSpendingExtraKey)
			} else {
				require.Equal(t, stored == "true", merged[service.AccountWindowCreditSpendingExtraKey])
			}
			require.Equal(t, "keep", merged["unrelated"])
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
