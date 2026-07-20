package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"

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

func TestUserAccountWindowQuotaRepository_ListByUserExcludesSoftDeletedAccounts(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	mock.ExpectQuery("FROM user_account_window_quotas q\\s+JOIN accounts a ON a\\.id = q\\.account_id\\s+WHERE q\\.user_id = \\$1 AND q\\.deleted_at IS NULL AND a\\.deleted_at IS NULL").
		WithArgs(int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "account_id", "window_type", "limit_percent", "attributed_percent", "window_reset_at", "donate_pool_fraction"}).
			AddRow(1, 7, service.WindowType5h, 46, 5, nil, 0))

	rows, err := repo.ListByUser(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, int64(7), rows[0].AccountID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_ListAllWithUserExcludesSoftDeletedAccounts(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	mock.ExpectQuery("JOIN users u ON u\\.id = q\\.user_id\\s+JOIN accounts a ON a\\.id = q\\.account_id\\s+WHERE q\\.deleted_at IS NULL AND u\\.deleted_at IS NULL AND a\\.deleted_at IS NULL").
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "email", "username", "account_id", "window_type", "limit_percent", "attributed_percent", "window_reset_at", "donate_pool_fraction"}).
			AddRow(1, "a@example.com", "Alice", 7, service.WindowType5h, 46, 5, nil, 0))

	rows, err := repo.ListAllWithUser(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, int64(7), rows[0].AccountID)
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
	mock.ExpectExec("UPDATE user_account_window_quotas SET deleted_at").
		WithArgs(int64(7), sqlmock.AnyArg(), int64(1), int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	for _, window := range []string{service.WindowType5h, service.WindowType7d} {
		for _, userID := range []int64{1, 2} {
			mock.ExpectExec("INSERT INTO user_account_window_quotas").
				WithArgs(userID, int64(7), window, float64(46), sqlmock.AnyArg()).
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
