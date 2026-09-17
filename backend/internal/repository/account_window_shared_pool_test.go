package repository

import (
	"context"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountWindowSharedPoolRepositoryUpdatesOnlyAccountFlag(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		repo, mock := newWindowQuotaRepoSQLMock(t)
		mock.ExpectExec(`UPDATE accounts\s+SET extra = COALESCE\(extra, '\{\}'::jsonb\) \|\| jsonb_build_object[\s\S]*WHERE id = \$1 AND deleted_at IS NULL[\s\S]*platform = 'openai'[\s\S]*window_quota_shared`).
			WithArgs(int64(7), service.AccountWindowSharedPoolModeExtraKey, enabled).
			WillReturnResult(sqlmock.NewResult(0, 1))
		require.NoError(t, repo.SetAccountSharedPoolMode(context.Background(), 7, enabled))
		require.NoError(t, mock.ExpectationsWereMet())
	}
}

func TestAccountWindowSharedPoolRepositoryRejectsNonSharedAccount(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	mock.ExpectExec(`UPDATE accounts[\s\S]*window_quota_shared`).
		WithArgs(int64(7), service.AccountWindowSharedPoolModeExtraKey, true).
		WillReturnResult(sqlmock.NewResult(0, 0))
	require.ErrorIs(t, repo.SetAccountSharedPoolMode(context.Background(), 7, true), service.ErrAccountWindowInvalidMembers)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountWindowSharedPoolRepositoryReadsFlagWithMembership(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	mock.ExpectQuery(`SELECT[\s\S]*window_quota_shared_pool_mode[\s\S]*WHERE q.account_id = \$1`).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"user_id", "account_id", "window_type", "limit_percent", "attributed_percent", "window_reset_at", "donate_pool_fraction", "shared_pool_mode"}).
		AddRow(1, 7, "5h", 23, 30, nil, 0.5, true))
	rows, err := repo.ListByAccount(context.Background(), 7)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, rows[0].SharedPoolMode)
	require.Equal(t, 30.0, rows[0].AttributedPercent)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountWindowSharedPoolModeSurvivesStaleAccountEdit(t *testing.T) {
	for _, enabled := range []string{"true", "false"} {
		t.Run(enabled, func(t *testing.T) {
			repo, mock := newWindowQuotaRepoSQLMock(t)
			mock.ExpectQuery(`SELECT[\s\S]*window_quota_shared_pool_mode[\s\S]*FOR NO KEY UPDATE`).
				WithArgs(int64(7), service.PlatformOpenAI, service.AccountTypeOAuth, `{}`, nil).
				WillReturnRows(sqlmock.NewRows([]string{"identity", "ollama_identity", "proxy_identity", "probe", "rate_sync", "snapshot", "ollama_session", "ollama_auto", "ollama_snapshot", "shared_pool_mode"}).
					AddRow(true, false, true, nil, nil, nil, nil, nil, nil, []byte(enabled)))
			account := &service.Account{ID: 7, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
				Extra: map[string]any{service.AccountWindowSharedPoolModeExtraKey: enabled != "true", "unrelated": "keep"}}
			merged, err := lockAndMergeAccountProbeExtra(context.Background(), repo.client, account, nil, nil)
			require.NoError(t, err)
			require.Equal(t, enabled == "true", merged[service.AccountWindowSharedPoolModeExtraKey])
			require.Equal(t, "keep", merged["unrelated"])
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
