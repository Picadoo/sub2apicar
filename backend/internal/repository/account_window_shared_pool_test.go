package repository

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type jsonExtraMatcher struct {
	check func(map[string]any) bool
}

func (m jsonExtraMatcher) Match(v driver.Value) bool {
	raw, ok := v.(string)
	if !ok {
		return false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return false
	}
	return m.check(payload)
}

func TestAccountWindowSharedPoolRepositoryUpdatesOnlyAccountFlag(t *testing.T) {
	t.Run("disable", func(t *testing.T) {
		repo, mock := newWindowQuotaRepoSQLMock(t)
		mock.ExpectExec(`UPDATE accounts\s+SET extra = COALESCE\(extra, '\{\}'::jsonb\) \|\| jsonb_build_object[\s\S]*WHERE id = \$1 AND deleted_at IS NULL[\s\S]*platform = 'openai'[\s\S]*window_quota_shared`).
			WithArgs(int64(7), service.AccountWindowSharedPoolModeExtraKey, false).
			WillReturnResult(sqlmock.NewResult(0, 1))
		require.NoError(t, repo.SetAccountSharedPoolMode(context.Background(), 7, false))
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("enable with future 7d reset preserves window state", func(t *testing.T) {
		repo, mock := newWindowQuotaRepoSQLMock(t)
		futureReset := time.Now().Add(24 * time.Hour).UTC()
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT id FROM accounts[\s\S]*platform = 'openai'[\s\S]*window_quota_shared[\s\S]*FOR UPDATE`).
			WithArgs(int64(7)).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(7)))
		mock.ExpectQuery(`SELECT window_reset_at\s+FROM user_account_window_quotas[\s\S]*FOR UPDATE`).
			WithArgs(int64(7), service.WindowType7d).
			WillReturnRows(sqlmock.NewRows([]string{"window_reset_at"}).AddRow(futureReset))
		mock.ExpectExec(`UPDATE accounts\s+SET extra = COALESCE\(extra, '\{\}'::jsonb\) \|\| jsonb_build_object`).
			WithArgs(int64(7), service.AccountWindowSharedPoolModeExtraKey, true).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		require.NoError(t, repo.SetAccountSharedPoolMode(context.Background(), 7, true))
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("enable after 7d expired clears stale due 7d window first", func(t *testing.T) {
		repo, mock := newWindowQuotaRepoSQLMock(t)
		pastReset := time.Now().Add(-2 * time.Minute).UTC()
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT id FROM accounts[\s\S]*platform = 'openai'[\s\S]*window_quota_shared[\s\S]*FOR UPDATE`).
			WithArgs(int64(7)).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(7)))
		mock.ExpectQuery(`SELECT window_reset_at\s+FROM user_account_window_quotas[\s\S]*FOR UPDATE`).
			WithArgs(int64(7), service.WindowType7d).
			WillReturnRows(sqlmock.NewRows([]string{"window_reset_at"}).AddRow(pastReset))
		mock.ExpectExec(`UPDATE user_account_window_quotas\s+SET attributed_percent = 0, donate_pool_fraction = 0, window_reset_at = NULL`).
			WithArgs(int64(7), service.WindowType7d, sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 2))
		mock.ExpectExec(`UPDATE accounts\s+SET extra = \(COALESCE\(extra, '\{\}'::jsonb\) - \$2\) \|\| jsonb_build_object\(\$3::text, true\)`).
			WithArgs(int64(7), service.AccountWindowAttributionCheckpointExtraKey(service.WindowType7d), service.AccountWindowSharedPoolModeExtraKey).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		require.NoError(t, repo.SetAccountSharedPoolMode(context.Background(), 7, true))
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestAccountWindowSharedPoolRepositoryRejectsNonSharedAccount(t *testing.T) {
	t.Run("enable", func(t *testing.T) {
		repo, mock := newWindowQuotaRepoSQLMock(t)
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT id FROM accounts[\s\S]*platform = 'openai'[\s\S]*window_quota_shared[\s\S]*FOR UPDATE`).
			WithArgs(int64(7)).
			WillReturnRows(sqlmock.NewRows([]string{"id"}))
		mock.ExpectRollback()
		require.ErrorIs(t, repo.SetAccountSharedPoolMode(context.Background(), 7, true), service.ErrAccountWindowInvalidMembers)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("disable", func(t *testing.T) {
		repo, mock := newWindowQuotaRepoSQLMock(t)
		mock.ExpectExec(`UPDATE accounts[\s\S]*window_quota_shared`).
			WithArgs(int64(7), service.AccountWindowSharedPoolModeExtraKey, false).
			WillReturnResult(sqlmock.NewResult(0, 0))
		require.ErrorIs(t, repo.SetAccountSharedPoolMode(context.Background(), 7, false), service.ErrAccountWindowInvalidMembers)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestAccountWindowSharedPoolRepositoryListExcludesDue7dAccounts(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	mock.ExpectQuery(`SELECT a\.id FROM accounts a[\s\S]*window_quota_shared[\s\S]*due\.window_type = '7d'[\s\S]*due\.window_reset_at <= NOW\(\)[\s\S]*future\.window_reset_at > NOW\(\)`).
		WithArgs(service.AccountWindowSharedPoolModeExtraKey).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(7)))
	ids, err := repo.ListSharedPoolAccountIDs(context.Background())
	require.NoError(t, err)
	require.Equal(t, []int64{7}, ids)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountWindowSharedPoolAutoDisablesOnlyOn7dSnapshotRollover(t *testing.T) {
	for _, window := range []string{service.WindowType5h, service.WindowType7d} {
		t.Run(window, func(t *testing.T) {
			windowDuration := 5 * time.Hour
			if window == service.WindowType7d {
				windowDuration = 7 * 24 * time.Hour
			}
			repo, mock := newWindowQuotaRepoSQLMock(t)
			oldWindowStart := time.Date(2026, time.August, 2, 23, 0, 0, 0, time.UTC)
			oldResetAt := oldWindowStart.Add(windowDuration)
			oldObservedAt := oldWindowStart.Add(4 * time.Hour)
			current := service.NewAccountWindowAttributionCheckpoint(80, oldWindowStart, oldObservedAt, &oldResetAt)
			encoded, err := json.Marshal(current)
			require.NoError(t, err)

			windowStart := oldWindowStart.Add(windowDuration)
			resetAt := windowStart.Add(windowDuration)
			observedAt := windowStart.Add(time.Minute)
			candidate := service.NewAccountWindowAttributionCheckpoint(2, windowStart, observedAt, &resetAt)
			candidate.UsageLog = &service.AccountWindowUsageLogRef{ID: 100, CreatedAt: observedAt, UserID: 1, CostUSD: 1, Inserted: true}

			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT extra->>\$2,[\s\S]*codex_usage_observed_unix_nano[\s\S]*FOR UPDATE`).
				WithArgs(int64(29), service.AccountWindowAttributionCheckpointExtraKey(window), service.AccountWindowForceUnattributedExtraKey).
				WillReturnRows(sqlmock.NewRows([]string{"checkpoint", "observed_nano", "observed_at", "force_unattributed"}).
					AddRow(string(encoded), strconv.FormatInt(observedAt.UnixNano(), 10), observedAt.Format(time.RFC3339Nano), false))
			mock.ExpectQuery(`SELECT q.id, q.user_id, q.attributed_percent, q.window_reset_at[\s\S]*FOR UPDATE`).
				WithArgs(int64(29), window).
				WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "attributed_percent", "window_reset_at"}).
					AddRow(int64(11), int64(1), 40.0, oldResetAt))
			mock.ExpectExec(`UPDATE user_account_window_quotas[\s\S]*attributed_percent = 0[\s\S]*donate_pool_fraction = 0`).
				WithArgs(int64(29), window, resetAt, sqlmock.AnyArg()).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`UPDATE accounts[\s\S]*extra = COALESCE\(extra`).
				WithArgs(int64(29), jsonExtraMatcher{check: func(payload map[string]any) bool {
					if _, hasCredit := payload[service.AccountWindowCreditSpendingExtraKey]; hasCredit {
						return false
					}
					val, hasSharedPool := payload[service.AccountWindowSharedPoolModeExtraKey]
					if window == service.WindowType7d {
						return hasSharedPool && val == false
					}
					return !hasSharedPool
				}}).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()

			_, applied, err := repo.ApplyWindowSharesFromCheckpoint(context.Background(), 29, window, candidate, &resetAt)
			require.NoError(t, err)
			require.True(t, applied)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestAccountWindowSharedPoolNotDisabledWhenAwaitingFirst7dBoundary(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	windowStart := time.Date(2026, time.August, 9, 23, 0, 0, 0, time.UTC)
	resetAt := windowStart.Add(7 * 24 * time.Hour)
	observedAt := windowStart.Add(time.Minute)
	candidate := service.NewAccountWindowAttributionCheckpoint(1, windowStart, observedAt, &resetAt)
	candidate.UsageLog = &service.AccountWindowUsageLogRef{ID: 101, CreatedAt: observedAt, UserID: 1, CostUSD: 1, Inserted: true}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT extra->>\$2,[\s\S]*codex_usage_observed_unix_nano[\s\S]*FOR UPDATE`).
		WithArgs(int64(29), service.AccountWindowAttributionCheckpointExtraKey(service.WindowType7d), service.AccountWindowForceUnattributedExtraKey).
		WillReturnRows(sqlmock.NewRows([]string{"checkpoint", "observed_nano", "observed_at", "force_unattributed"}).
			AddRow(nil, strconv.FormatInt(observedAt.UnixNano(), 10), observedAt.Format(time.RFC3339Nano), false))
	mock.ExpectQuery(`SELECT q.id, q.user_id, q.attributed_percent, q.window_reset_at[\s\S]*FOR UPDATE`).
		WithArgs(int64(29), service.WindowType7d).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "attributed_percent", "window_reset_at"}).
			AddRow(int64(11), int64(1), 0.0, nil))
	mock.ExpectExec(`UPDATE user_account_window_quotas\s+SET window_reset_at = \$3`).
		WithArgs(int64(29), service.WindowType7d, resetAt, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE accounts[\s\S]*extra = COALESCE\(extra`).
		WithArgs(int64(29), jsonExtraMatcher{check: func(payload map[string]any) bool {
			_, hasSharedPool := payload[service.AccountWindowSharedPoolModeExtraKey]
			return !hasSharedPool
		}}).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	_, applied, err := repo.ApplyWindowSharesFromCheckpoint(context.Background(), 29, service.WindowType7d, candidate, &resetAt)
	require.NoError(t, err)
	require.True(t, applied)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountWindowSharedPoolDisabledWhenDue7dClearedOnDonate(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	pastReset := time.Now().Add(-2 * time.Minute).UTC()

	mock.ExpectBegin()
	expectSharedAccountWindowQuotaLock(mock, 29)
	mock.ExpectQuery(`SELECT q\.user_id, q\.account_id, q\.window_type, q\.limit_percent, q\.attributed_percent, q\.window_reset_at, COALESCE\(q\.donate_pool_fraction, 0\)[\s\S]*FOR UPDATE`).
		WithArgs(int64(29), service.WindowType7d).
		WillReturnRows(sqlmock.NewRows([]string{
			"user_id", "account_id", "window_type", "limit_percent", "attributed_percent", "window_reset_at",
			"donate_pool_fraction", "shared_pool_mode", "credit_spending_enabled",
		}).
			AddRow(int64(1), int64(29), service.WindowType7d, 23.0, 20.0, pastReset, 0.0, true, false))
	mock.ExpectExec(`UPDATE user_account_window_quotas\s+SET attributed_percent = 0, donate_pool_fraction = 0, window_reset_at = NULL`).
		WithArgs(int64(29), service.WindowType7d, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE accounts\s+SET extra = \(COALESCE\(extra, '\{\}'::jsonb\) - \$2\) \|\| jsonb_build_object\(\$3::text, false\)`).
		WithArgs(int64(29), service.AccountWindowAttributionCheckpointExtraKey(service.WindowType7d), service.AccountWindowSharedPoolModeExtraKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE user_account_window_quotas\s+SET donate_pool_fraction = \$4`).
		WithArgs(int64(1), int64(29), service.WindowType7d, 0.5, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := repo.SetDonatePoolFraction(context.Background(), 1, 29, service.WindowType7d, 0.5, service.DefaultAccountWindowLimitPercent)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountWindowSharedPoolRepositoryReadsFlagWithMembership(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	mock.ExpectQuery(`SELECT[\s\S]*window_quota_shared_pool_mode[\s\S]*WHERE q.account_id = \$1`).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"user_id", "account_id", "window_type", "limit_percent", "attributed_percent", "window_reset_at", "donate_pool_fraction", "shared_pool_mode", "credit_spending_enabled"}).
		AddRow(1, 7, "5h", 23, 30, nil, 0.5, true, false))
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
				WillReturnRows(sqlmock.NewRows([]string{"identity", "ollama_identity", "proxy_identity", "probe", "rate_sync", "snapshot", "ollama_session", "ollama_auto", "ollama_snapshot", "opencode_identity", "opencode_auto", "opencode_snapshot", "shared_pool_mode", "grok_observed_models", "upstream_model_metadata", "credit_spending_enabled"}).
					AddRow(true, false, true, nil, nil, nil, nil, nil, nil, false, nil, nil, []byte(enabled), nil, nil, nil))
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
