package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

func newWindowQuotaRepoSQLMock(t *testing.T) (*userAccountWindowQuotaRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	drv := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(drv))
	t.Cleanup(func() {
		_ = client.Close()
		_ = db.Close()
	})
	return &userAccountWindowQuotaRepository{client: client}, mock
}

func TestUserAccountWindowQuotaRepository_GetMaxConfiguredSumForWindow(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(MAX(configured_sum), 0)")).
		WithArgs(service.WindowType5h).
		WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(93.5))

	got, err := repo.GetMaxConfiguredSumForWindow(context.Background(), service.WindowType5h)
	require.NoError(t, err)
	require.InDelta(t, 93.5, got, 1e-9)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_RecomputeFromCheckpointUsesTokenShares(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	checkpointAt := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	windowStart := checkpointAt.Add(-time.Hour)
	baseShares := map[int64]float64{1: 6.5, 31: 6.5, 32: 6.5, 33: 6.5}

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT q\\.id, q\\.user_id").
		WithArgs(int64(29), service.WindowType5h).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id"}).
			AddRow(101, 1).
			AddRow(102, 31).
			AddRow(103, 32).
			AddRow(104, 33))
	mock.ExpectQuery("SELECT user_id,\\s+COALESCE\\(SUM\\(input_tokens").
		WithArgs(int64(29), checkpointAt, int64(1), int64(31), int64(32), int64(33)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "tokens"}).
			AddRow(1, 300).
			AddRow(31, 100))
	for i, id := range []int64{101, 102, 103, 104} {
		values := []float64{9.5, 7.5, 6.5, 6.5}
		mock.ExpectExec("UPDATE user_account_window_quotas\\s+SET attributed_percent").
			WithArgs(id, values[i], nil, sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()

	err := repo.RecomputeWindowSharesFromCheckpoint(context.Background(), 29, service.WindowType5h, 30, windowStart, checkpointAt, baseShares, nil)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_RecomputeFromCheckpointJitterDoesNotTransferShares(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	checkpointAt := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	windowStart := checkpointAt.Add(-time.Hour)
	baseShares := map[int64]float64{1: 1.25, 31: 1.25, 32: 1.25, 33: 1.25}

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT q\\.id, q\\.user_id").
		WithArgs(int64(29), service.WindowType7d).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id"}).
			AddRow(101, 1).
			AddRow(102, 31).
			AddRow(103, 32).
			AddRow(104, 33))
	mock.ExpectQuery("SELECT user_id,\\s+COALESCE\\(SUM\\(input_tokens").
		WithArgs(int64(29), checkpointAt, int64(1), int64(31), int64(32), int64(33)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "tokens"}).AddRow(1, 10000))
	for _, id := range []int64{101, 102, 103, 104} {
		mock.ExpectExec("UPDATE user_account_window_quotas\\s+SET attributed_percent").
			WithArgs(id, float64(1), nil, sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()

	err := repo.RecomputeWindowSharesFromCheckpoint(context.Background(), 29, service.WindowType7d, 4, windowStart, checkpointAt, baseShares, nil)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_GetByUserAccountWindowRequiresSharedAccount(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	mock.ExpectQuery("FROM user_account_window_quotas q\\s+JOIN accounts a ON a\\.id = q\\.account_id[\\s\\S]*COALESCE\\(a\\.extra->>'window_quota_shared', 'false'\\) = 'true'").
		WithArgs(int64(1), int64(7), service.WindowType5h).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "account_id", "window_type", "limit_percent", "attributed_percent", "window_reset_at", "donate_pool_fraction"}))

	row, err := repo.GetByUserAccountWindow(context.Background(), 1, 7, service.WindowType5h)
	require.NoError(t, err)
	require.Nil(t, row)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_ListByUserExcludesPrivateAndSoftDeletedAccounts(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	mock.ExpectQuery("FROM user_account_window_quotas q\\s+JOIN accounts a ON a\\.id = q\\.account_id\\s+WHERE q\\.user_id = \\$1 AND q\\.deleted_at IS NULL AND a\\.deleted_at IS NULL\\s+AND COALESCE\\(a\\.extra->>'window_quota_shared', 'false'\\) = 'true'").
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "account_id", "window_type", "limit_percent", "attributed_percent", "window_reset_at", "donate_pool_fraction"}).
			AddRow(1, 7, service.WindowType5h, 46, 5, nil, 0))

	rows, err := repo.ListByUser(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, int64(7), rows[0].AccountID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_ListAllWithUserExcludesPrivateAndSoftDeletedAccounts(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	mock.ExpectQuery("JOIN users u ON u\\.id = q\\.user_id\\s+JOIN accounts a ON a\\.id = q\\.account_id\\s+WHERE q\\.deleted_at IS NULL AND u\\.deleted_at IS NULL AND a\\.deleted_at IS NULL\\s+AND COALESCE\\(a\\.extra->>'window_quota_shared', 'false'\\) = 'true'").
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "email", "username", "account_id", "window_type", "limit_percent", "attributed_percent", "window_reset_at", "donate_pool_fraction"}).
			AddRow(1, "a@example.com", "Alice", 7, service.WindowType5h, 46, 5, nil, 0))

	rows, err := repo.ListAllWithUser(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, int64(7), rows[0].AccountID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_ListSharedAccountIDsIncludesAccountsWithoutQuotaRows(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	mock.ExpectQuery("SELECT id\\s+FROM accounts[\\s\\S]*window_quota_shared").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(29).AddRow(31).AddRow(33))

	ids, err := repo.ListSharedAccountIDs(context.Background())
	require.NoError(t, err)
	require.Equal(t, []int64{29, 31, 33}, ids)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_ListPeerSharedAccountMemberUserIDs(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	mock.ExpectQuery("FROM account_groups target_group[\\s\\S]*peer_account[\\s\\S]*user_account_window_quotas").
		WithArgs(int64(34)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(1).AddRow(31).AddRow(32).AddRow(33))

	ids, err := repo.ListPeerSharedAccountMemberUserIDs(context.Background(), 34)
	require.NoError(t, err)
	require.Equal(t, []int64{1, 31, 32, 33}, ids)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_SetDonatePoolFractionRejectsBorrowedReclaim(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT q\\.user_id[\\s\\S]*FOR UPDATE").
		WithArgs(int64(1), service.WindowType5h).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "account_id", "window_type", "limit_percent", "attributed_percent", "window_reset_at", "donate_pool_fraction"}).
			AddRow(1, 1, service.WindowType5h, 23, 35, nil, 0).
			AddRow(2, 1, service.WindowType5h, 23, 0, nil, 1))
	mock.ExpectRollback()

	err := repo.SetDonatePoolFraction(context.Background(), 2, 1, service.WindowType5h, 0, 23)
	require.ErrorIs(t, err, service.ErrAccountWindowDonationInUse)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_SetDonatePoolFractionAllowsUnusedReclaim(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	minimum := 12.0 / 23.0
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT q\\.user_id[\\s\\S]*FOR UPDATE").
		WithArgs(int64(1), service.WindowType5h).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "account_id", "window_type", "limit_percent", "attributed_percent", "window_reset_at", "donate_pool_fraction"}).
			AddRow(1, 1, service.WindowType5h, 23, 35, nil, 0).
			AddRow(2, 1, service.WindowType5h, 23, 0, nil, 1))
	mock.ExpectExec("INSERT INTO user_account_window_quotas").
		WithArgs(int64(2), int64(1), service.WindowType5h, float64(23), minimum, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err := repo.SetDonatePoolFraction(context.Background(), 2, 1, service.WindowType5h, minimum, 23)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_SyncAccountMembers_CreatesUnusedUsersAndRebalances(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM accounts").
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM users").
		WithArgs(int64(1), int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery("SELECT window_type, COALESCE\\(SUM\\(attributed_percent\\), 0\\), MAX\\(window_reset_at\\)").
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"window_type", "total", "reset_at"}).
			AddRow(service.WindowType5h, 30, nil).
			AddRow(service.WindowType7d, 10, nil))
	mock.ExpectExec("UPDATE user_account_window_quotas SET deleted_at").
		WithArgs(int64(7), sqlmock.AnyArg(), int64(1), int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	usedShares := map[string]float64{service.WindowType5h: 15, service.WindowType7d: 5}
	for _, window := range []string{service.WindowType5h, service.WindowType7d} {
		for _, userID := range []int64{1, 2} {
			mock.ExpectExec("INSERT INTO user_account_window_quotas").
				WithArgs(userID, int64(7), window, float64(46), usedShares[window], nil, sqlmock.AnyArg()).
				WillReturnResult(sqlmock.NewResult(1, 1))
		}
	}
	mock.ExpectCommit()

	results, err := repo.SyncAccountMembers(context.Background(), 7, []int64{1, 2}, 92, 92)
	require.NoError(t, err)
	require.Equal(t, []service.AccountWindowEqualizationResult{
		{WindowType: service.WindowType5h, ActiveMemberCount: 2, SharePercent: 46, CeilingPercent: 92},
		{WindowType: service.WindowType7d, ActiveMemberCount: 2, SharePercent: 46, CeilingPercent: 92},
	}, results)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_EqualizeActiveMemberLimits_CommitsBothWindows(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT q\\.user_id\\s+FROM user_account_window_quotas q\\s+JOIN accounts a").
		WithArgs(int64(7), service.WindowType5h).
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(1).AddRow(2).AddRow(3))
	mock.ExpectExec("UPDATE user_account_window_quotas").
		WithArgs(int64(7), service.WindowType5h, float64(30.6666), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectQuery("SELECT q\\.user_id\\s+FROM user_account_window_quotas q\\s+JOIN accounts a").
		WithArgs(int64(7), service.WindowType7d).
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(1).AddRow(2).AddRow(3).AddRow(4))
	mock.ExpectExec("UPDATE user_account_window_quotas").
		WithArgs(int64(7), service.WindowType7d, float64(23), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 4))
	mock.ExpectCommit()

	results, err := repo.EqualizeActiveMemberLimits(context.Background(), 7, 92, 92)
	require.NoError(t, err)
	require.Equal(t, []service.AccountWindowEqualizationResult{
		{WindowType: service.WindowType5h, ActiveMemberCount: 3, SharePercent: 30.6666, CeilingPercent: 92},
		{WindowType: service.WindowType7d, ActiveMemberCount: 4, SharePercent: 23, CeilingPercent: 92},
	}, results)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_EqualizeActiveMemberLimits_RollsBackWhenSecondWindowFails(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT q\\.user_id\\s+FROM user_account_window_quotas q\\s+JOIN accounts a").
		WithArgs(int64(7), service.WindowType5h).
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(1).AddRow(2))
	mock.ExpectExec("UPDATE user_account_window_quotas").
		WithArgs(int64(7), service.WindowType5h, float64(46), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectQuery("SELECT q\\.user_id\\s+FROM user_account_window_quotas q\\s+JOIN accounts a").
		WithArgs(int64(7), service.WindowType7d).
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(1).AddRow(2))
	mock.ExpectExec("UPDATE user_account_window_quotas").
		WithArgs(int64(7), service.WindowType7d, float64(46), sqlmock.AnyArg()).
		WillReturnError(errors.New("write failed"))
	mock.ExpectRollback()

	results, err := repo.EqualizeActiveMemberLimits(context.Background(), 7, 92, 92)
	require.ErrorContains(t, err, "write failed")
	require.Nil(t, results)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_EqualizeActiveMemberLimits_RollsBackWhenWindowHasNoMembers(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT q\\.user_id\\s+FROM user_account_window_quotas q\\s+JOIN accounts a").
		WithArgs(int64(7), service.WindowType5h).
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}))
	mock.ExpectRollback()

	_, err := repo.EqualizeActiveMemberLimits(context.Background(), 7, 92, 92)
	require.ErrorIs(t, err, service.ErrAccountWindowNoActiveMembers)
	require.NoError(t, mock.ExpectationsWereMet())
}
