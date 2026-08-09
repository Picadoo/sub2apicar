package repository

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

type captureStringArgument struct {
	value *string
}

func (m captureStringArgument) Match(value driver.Value) bool {
	text, ok := value.(string)
	if ok && m.value != nil {
		*m.value = text
	}
	return ok
}

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

func TestUserAccountWindowQuotaRepository_ResetWindowForAccountIfDueIsConditional(t *testing.T) {
	dueAt := time.Date(2026, time.August, 2, 10, 0, 0, 0, time.UTC)

	t.Run("all rows still due", func(t *testing.T) {
		repo, mock := newWindowQuotaRepoSQLMock(t)
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT id FROM accounts[\s\S]*window_quota_shared[\s\S]*FOR UPDATE`).
			WithArgs(int64(29)).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(29)))
		mock.ExpectExec(`UPDATE user_account_window_quotas AS target[\s\S]*EXISTS[\s\S]*due.window_reset_at <= \$3[\s\S]*NOT EXISTS[\s\S]*future.window_reset_at > \$3`).
			WithArgs(int64(29), service.WindowType5h, dueAt, sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 2))
		mock.ExpectExec(`UPDATE accounts[\s\S]*extra = COALESCE\(extra`).
			WithArgs(int64(29), service.AccountWindowAttributionCheckpointExtraKey(service.WindowType5h)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()

		reset, err := repo.ResetWindowForAccountIfDue(context.Background(), 29, service.WindowType5h, dueAt)
		require.NoError(t, err)
		require.True(t, reset)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("newer window superseded scan", func(t *testing.T) {
		repo, mock := newWindowQuotaRepoSQLMock(t)
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT id FROM accounts[\s\S]*window_quota_shared[\s\S]*FOR UPDATE`).
			WithArgs(int64(29)).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(29)))
		mock.ExpectExec(`UPDATE user_account_window_quotas AS target[\s\S]*NOT EXISTS`).
			WithArgs(int64(29), service.WindowType5h, dueAt, sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectCommit()

		reset, err := repo.ResetWindowForAccountIfDue(context.Background(), 29, service.WindowType5h, dueAt)
		require.NoError(t, err)
		require.False(t, reset)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestUserAccountWindowQuotaRepository_ApplyWindowSharesRejectsCandidateOlderThanCanonicalSnapshot(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	observedAt := time.Date(2026, time.August, 2, 8, 0, 0, 0, time.UTC)
	canonicalObservedAt := observedAt.Add(5 * time.Minute)
	resetAt := observedAt.Add(2 * time.Hour)
	windowStart := resetAt.Add(-5 * time.Hour)
	candidate := service.NewAccountWindowAttributionCheckpoint(60, windowStart, observedAt, &resetAt)
	stored := service.NewAccountWindowAttributionCheckpoint(64, windowStart, canonicalObservedAt, &resetAt)
	rawExtra, err := json.Marshal(stored)
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT extra->>\$2,[\s\S]*codex_usage_observed_unix_nano[\s\S]*FOR UPDATE`).
		WithArgs(int64(29), service.AccountWindowAttributionCheckpointExtraKey(service.WindowType5h), service.AccountWindowForceUnattributedExtraKey).
		WillReturnRows(sqlmock.NewRows([]string{"checkpoint", "observed_nano", "observed_at", "force_unattributed"}).
			AddRow(string(rawExtra), strconv.FormatInt(canonicalObservedAt.UnixNano(), 10), canonicalObservedAt.Format(time.RFC3339Nano), false))
	mock.ExpectQuery(`SELECT q.id, q.user_id, q.attributed_percent, q.window_reset_at[\s\S]*FOR UPDATE`).
		WithArgs(int64(29), service.WindowType5h).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "attributed_percent", "window_reset_at"}))
	mock.ExpectCommit()

	winner, applied, err := repo.ApplyWindowSharesFromCheckpoint(context.Background(), 29, service.WindowType5h, candidate, &resetAt)
	require.NoError(t, err)
	require.False(t, applied)
	require.Equal(t, stored, winner)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_ApplyWindowSharesSkipsNonSharedAccount(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	observedAt := time.Date(2026, time.August, 2, 8, 0, 0, 0, time.UTC)
	resetAt := observedAt.Add(2 * time.Hour)
	candidate := service.NewAccountWindowAttributionCheckpoint(60, resetAt.Add(-5*time.Hour), observedAt, &resetAt)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT extra->>\$2,[\s\S]*codex_usage_observed_unix_nano[\s\S]*FOR UPDATE`).
		WithArgs(int64(50), service.AccountWindowAttributionCheckpointExtraKey(service.WindowType5h), service.AccountWindowForceUnattributedExtraKey).
		WillReturnRows(sqlmock.NewRows([]string{"checkpoint", "observed_nano", "observed_at", "force_unattributed"}))
	mock.ExpectCommit()

	winner, applied, err := repo.ApplyWindowSharesFromCheckpoint(context.Background(), 50, service.WindowType5h, candidate, &resetAt)
	require.NoError(t, err)
	require.False(t, applied)
	require.Equal(t, candidate, winner)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_ApplyWindowSharesAccumulatesPendingWithoutRewritingHistory(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	windowStart := time.Date(2026, time.August, 3, 4, 0, 0, 0, time.UTC)
	resetAt := windowStart.Add(5 * time.Hour)
	previousObservedAt := windowStart.Add(2 * time.Hour)
	observedAt := previousObservedAt.Add(time.Minute)
	seenAt := windowStart.Add(time.Hour)
	logAt := seenAt.Add(time.Minute)
	current := service.NewAccountWindowAttributionCheckpoint(10, windowStart, previousObservedAt, &resetAt)
	current.UnattributedPercent = 2
	current.SeenThroughCreatedAt = seenAt
	current.SeenThroughUsageLogID = 50
	encoded, err := json.Marshal(current)
	require.NoError(t, err)
	candidate := service.NewAccountWindowAttributionCheckpoint(10, windowStart, observedAt, &resetAt)
	candidate.UsageLog = &service.AccountWindowUsageLogRef{ID: 100, CreatedAt: logAt, UserID: 1, CostUSD: 3, Inserted: true}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT extra->>\$2,[\s\S]*codex_usage_observed_unix_nano[\s\S]*FOR UPDATE`).
		WithArgs(int64(29), service.AccountWindowAttributionCheckpointExtraKey(service.WindowType5h), service.AccountWindowForceUnattributedExtraKey).
		WillReturnRows(sqlmock.NewRows([]string{"checkpoint", "observed_nano", "observed_at", "force_unattributed"}).
			AddRow(string(encoded), strconv.FormatInt(observedAt.UnixNano(), 10), observedAt.Format(time.RFC3339Nano), false))
	mock.ExpectQuery(`SELECT q.id, q.user_id, q.attributed_percent, q.window_reset_at[\s\S]*FOR UPDATE`).
		WithArgs(int64(29), service.WindowType5h).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "attributed_percent", "window_reset_at"}).
			AddRow(int64(11), int64(1), 6.0, resetAt).
			AddRow(int64(12), int64(2), 2.0, resetAt))
	mock.ExpectQuery(`SELECT user_id,[\s\S]*SUM\(GREATEST[\s\S]*\(created_at, id\) >`).
		WithArgs(int64(29), windowStart, resetAt, seenAt, int64(50), logAt, int64(100)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "cost"}).
			AddRow(int64(1), 3.0).
			AddRow(int64(2), 1.0))
	mock.ExpectExec(`UPDATE accounts[\s\S]*extra = COALESCE\(extra`).
		WithArgs(int64(29), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	winner, applied, err := repo.ApplyWindowSharesFromCheckpoint(context.Background(), 29, service.WindowType5h, candidate, &resetAt)
	require.NoError(t, err)
	require.True(t, applied)
	require.InDelta(t, 10, winner.LatestOfficialPercent, 1e-9)
	require.InDelta(t, 2, winner.UnattributedPercent, 1e-9)
	require.Equal(t, map[int64]float64{1: 3, 2: 1}, winner.PendingBasisByUser)
	require.Equal(t, logAt, winner.SeenThroughCreatedAt)
	require.Equal(t, int64(100), winner.SeenThroughUsageLogID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_ApplyWindowSharesSettlesOnlyOfficialDelta(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	windowStart := time.Date(2026, time.August, 3, 4, 0, 0, 0, time.UTC)
	resetAt := windowStart.Add(5 * time.Hour)
	previousObservedAt := windowStart.Add(2 * time.Hour)
	observedAt := previousObservedAt.Add(time.Minute)
	seenAt := windowStart.Add(time.Hour)
	current := service.NewAccountWindowAttributionCheckpoint(10, windowStart, previousObservedAt, &resetAt)
	current.UnattributedPercent = 2
	current.PendingBasisByUser = map[int64]float64{1: 3, 2: 1}
	current.PendingUnattributedBasisUSD = 1
	current.SeenThroughCreatedAt = seenAt
	current.SeenThroughUsageLogID = 50
	encoded, err := json.Marshal(current)
	require.NoError(t, err)
	candidate := service.NewAccountWindowAttributionCheckpoint(15, windowStart, observedAt, &resetAt)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT extra->>\$2,[\s\S]*codex_usage_observed_unix_nano[\s\S]*FOR UPDATE`).
		WithArgs(int64(29), service.AccountWindowAttributionCheckpointExtraKey(service.WindowType5h), service.AccountWindowForceUnattributedExtraKey).
		WillReturnRows(sqlmock.NewRows([]string{"checkpoint", "observed_nano", "observed_at", "force_unattributed"}).
			AddRow(string(encoded), strconv.FormatInt(observedAt.UnixNano(), 10), observedAt.Format(time.RFC3339Nano), false))
	mock.ExpectQuery(`SELECT q.id, q.user_id, q.attributed_percent, q.window_reset_at[\s\S]*FOR UPDATE`).
		WithArgs(int64(29), service.WindowType5h).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "attributed_percent", "window_reset_at"}).
			AddRow(int64(11), int64(1), 6.0, resetAt).
			AddRow(int64(12), int64(2), 2.0, resetAt))
	mock.ExpectQuery(`SELECT created_at, id[\s\S]*ORDER BY created_at DESC`).
		WithArgs(int64(29), windowStart, resetAt, observedAt).
		WillReturnRows(sqlmock.NewRows([]string{"created_at", "id"}))
	mock.ExpectExec(`UPDATE user_account_window_quotas[\s\S]*attributed_percent = attributed_percent \+ \$2`).
		WithArgs(int64(11), 3.0, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE user_account_window_quotas[\s\S]*attributed_percent = attributed_percent \+ \$2`).
		WithArgs(int64(12), 1.0, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE accounts[\s\S]*extra = COALESCE\(extra`).
		WithArgs(int64(29), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	winner, applied, err := repo.ApplyWindowSharesFromCheckpoint(context.Background(), 29, service.WindowType5h, candidate, &resetAt)
	require.NoError(t, err)
	require.True(t, applied)
	require.InDelta(t, 15, winner.LatestOfficialPercent, 1e-9)
	require.InDelta(t, 3, winner.UnattributedPercent, 1e-9)
	require.Empty(t, winner.PendingBasisByUser)
	require.Zero(t, winner.PendingUnattributedBasisUSD)
	require.Equal(t, seenAt, winner.SeenThroughCreatedAt)
	require.Equal(t, int64(50), winner.SeenThroughUsageLogID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_ApplyWindowSharesForceUnattributedNeverChargesMembers(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	windowStart := time.Date(2026, time.August, 3, 4, 0, 0, 0, time.UTC)
	resetAt := windowStart.Add(5 * time.Hour)
	previousObservedAt := windowStart.Add(2 * time.Hour)
	observedAt := previousObservedAt.Add(time.Minute)
	seenAt := windowStart.Add(time.Hour)
	logAt := seenAt.Add(time.Minute)
	current := service.NewAccountWindowAttributionCheckpoint(10, windowStart, previousObservedAt, &resetAt)
	current.UnattributedPercent = 2
	current.PendingBasisByUser = map[int64]float64{1: 3, 2: 1}
	current.PendingUnattributedBasisUSD = 1
	current.SeenThroughCreatedAt = seenAt
	current.SeenThroughUsageLogID = 50
	encoded, err := json.Marshal(current)
	require.NoError(t, err)
	candidate := service.NewAccountWindowAttributionCheckpoint(15, windowStart, observedAt, &resetAt)
	candidate.UsageLog = &service.AccountWindowUsageLogRef{ID: 100, CreatedAt: logAt, UserID: 1, CostUSD: 3, Inserted: true}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT extra->>\$2,[\s\S]*codex_usage_observed_unix_nano[\s\S]*FOR UPDATE`).
		WithArgs(int64(29), service.AccountWindowAttributionCheckpointExtraKey(service.WindowType5h), service.AccountWindowForceUnattributedExtraKey).
		WillReturnRows(sqlmock.NewRows([]string{"checkpoint", "observed_nano", "observed_at", "force_unattributed"}).
			AddRow(string(encoded), strconv.FormatInt(observedAt.UnixNano(), 10), observedAt.Format(time.RFC3339Nano), true))
	mock.ExpectQuery(`SELECT q.id, q.user_id, q.attributed_percent, q.window_reset_at[\s\S]*FOR UPDATE`).
		WithArgs(int64(29), service.WindowType5h).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "attributed_percent", "window_reset_at"}).
			AddRow(int64(11), int64(1), 6.0, resetAt).
			AddRow(int64(12), int64(2), 2.0, resetAt))
	mock.ExpectExec(`UPDATE accounts[\s\S]*extra = COALESCE\(extra`).
		WithArgs(int64(29), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	winner, applied, err := repo.ApplyWindowSharesFromCheckpoint(context.Background(), 29, service.WindowType5h, candidate, &resetAt)
	require.NoError(t, err)
	require.True(t, applied)
	require.InDelta(t, 15, winner.LatestOfficialPercent, 1e-9)
	require.InDelta(t, 7, winner.UnattributedPercent, 1e-9)
	require.Empty(t, winner.PendingBasisByUser)
	require.Zero(t, winner.PendingUnattributedBasisUSD)
	require.Equal(t, logAt, winner.SeenThroughCreatedAt)
	require.Equal(t, int64(100), winner.SeenThroughUsageLogID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_ApplyWindowSharesForceUnattributedDuplicateRefreshIsIdempotent(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	windowStart := time.Date(2026, time.August, 3, 4, 0, 0, 0, time.UTC)
	resetAt := windowStart.Add(5 * time.Hour)
	observedAt := windowStart.Add(2 * time.Hour)
	seenAt := windowStart.Add(time.Hour)
	current := service.NewAccountWindowAttributionCheckpoint(15, windowStart, observedAt, &resetAt)
	current.UnattributedPercent = 15
	current.SeenThroughCreatedAt = seenAt
	current.SeenThroughUsageLogID = 100
	encoded, err := json.Marshal(current)
	require.NoError(t, err)
	candidate := service.NewAccountWindowAttributionCheckpoint(15, windowStart, observedAt, &resetAt)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT extra->>\$2,[\s\S]*codex_usage_observed_unix_nano[\s\S]*FOR UPDATE`).
		WithArgs(int64(29), service.AccountWindowAttributionCheckpointExtraKey(service.WindowType5h), service.AccountWindowForceUnattributedExtraKey).
		WillReturnRows(sqlmock.NewRows([]string{"checkpoint", "observed_nano", "observed_at", "force_unattributed"}).
			AddRow(string(encoded), strconv.FormatInt(observedAt.UnixNano(), 10), observedAt.Format(time.RFC3339Nano), true))
	mock.ExpectQuery(`SELECT q.id, q.user_id, q.attributed_percent, q.window_reset_at[\s\S]*FOR UPDATE`).
		WithArgs(int64(29), service.WindowType5h).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "attributed_percent", "window_reset_at"}).
			AddRow(int64(11), int64(1), 0.0, resetAt))
	mock.ExpectQuery(`SELECT created_at, id[\s\S]*ORDER BY created_at DESC`).
		WithArgs(int64(29), windowStart, resetAt, observedAt).
		WillReturnRows(sqlmock.NewRows([]string{"created_at", "id"}))
	mock.ExpectCommit()

	winner, applied, err := repo.ApplyWindowSharesFromCheckpoint(context.Background(), 29, service.WindowType5h, candidate, &resetAt)
	require.NoError(t, err)
	require.False(t, applied)
	require.Equal(t, current, winner)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_ApplyWindowSharesDuplicateLogIsIdempotent(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	windowStart := time.Date(2026, time.August, 3, 4, 0, 0, 0, time.UTC)
	resetAt := windowStart.Add(5 * time.Hour)
	observedAt := windowStart.Add(2 * time.Hour)
	seenAt := windowStart.Add(time.Hour)
	current := service.NewAccountWindowAttributionCheckpoint(10, windowStart, observedAt, &resetAt)
	current.UnattributedPercent = 2
	current.PendingBasisByUser = map[int64]float64{1: 3}
	current.SeenThroughCreatedAt = seenAt
	current.SeenThroughUsageLogID = 50
	encoded, err := json.Marshal(current)
	require.NoError(t, err)
	candidate := service.NewAccountWindowAttributionCheckpoint(10, windowStart, observedAt, &resetAt)
	candidate.UsageLog = &service.AccountWindowUsageLogRef{ID: 50, CreatedAt: seenAt, UserID: 1, CostUSD: 3, Inserted: false}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT extra->>\$2,[\s\S]*codex_usage_observed_unix_nano[\s\S]*FOR UPDATE`).
		WithArgs(int64(29), service.AccountWindowAttributionCheckpointExtraKey(service.WindowType5h), service.AccountWindowForceUnattributedExtraKey).
		WillReturnRows(sqlmock.NewRows([]string{"checkpoint", "observed_nano", "observed_at", "force_unattributed"}).
			AddRow(string(encoded), strconv.FormatInt(observedAt.UnixNano(), 10), observedAt.Format(time.RFC3339Nano), false))
	mock.ExpectQuery(`SELECT q.id, q.user_id, q.attributed_percent, q.window_reset_at[\s\S]*FOR UPDATE`).
		WithArgs(int64(29), service.WindowType5h).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "attributed_percent", "window_reset_at"}).
			AddRow(int64(11), int64(1), 6.0, resetAt))
	mock.ExpectCommit()

	winner, applied, err := repo.ApplyWindowSharesFromCheckpoint(context.Background(), 29, service.WindowType5h, candidate, &resetAt)
	require.NoError(t, err)
	require.False(t, applied)
	require.Equal(t, current, winner)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_ApplyWindowSharesLateInsertedLogBecomesUnattributedPending(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	windowStart := time.Date(2026, time.August, 3, 4, 0, 0, 0, time.UTC)
	resetAt := windowStart.Add(5 * time.Hour)
	observedAt := windowStart.Add(2 * time.Hour)
	seenAt := windowStart.Add(time.Hour)
	lateAt := seenAt.Add(-time.Minute)
	current := service.NewAccountWindowAttributionCheckpoint(10, windowStart, observedAt, &resetAt)
	current.UnattributedPercent = 2
	current.PendingBasisByUser = map[int64]float64{1: 3}
	current.SeenThroughCreatedAt = seenAt
	current.SeenThroughUsageLogID = 50
	encoded, err := json.Marshal(current)
	require.NoError(t, err)
	candidate := service.NewAccountWindowAttributionCheckpoint(10, windowStart, observedAt, &resetAt)
	candidate.UsageLog = &service.AccountWindowUsageLogRef{ID: 40, CreatedAt: lateAt, UserID: 1, CostUSD: 2.5, Inserted: true}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT extra->>\$2,[\s\S]*codex_usage_observed_unix_nano[\s\S]*FOR UPDATE`).
		WithArgs(int64(29), service.AccountWindowAttributionCheckpointExtraKey(service.WindowType5h), service.AccountWindowForceUnattributedExtraKey).
		WillReturnRows(sqlmock.NewRows([]string{"checkpoint", "observed_nano", "observed_at", "force_unattributed"}).
			AddRow(string(encoded), strconv.FormatInt(observedAt.UnixNano(), 10), observedAt.Format(time.RFC3339Nano), false))
	mock.ExpectQuery(`SELECT q.id, q.user_id, q.attributed_percent, q.window_reset_at[\s\S]*FOR UPDATE`).
		WithArgs(int64(29), service.WindowType5h).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "attributed_percent", "window_reset_at"}).
			AddRow(int64(11), int64(1), 6.0, resetAt))
	mock.ExpectExec(`UPDATE accounts[\s\S]*extra = COALESCE\(extra`).
		WithArgs(int64(29), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	winner, applied, err := repo.ApplyWindowSharesFromCheckpoint(context.Background(), 29, service.WindowType5h, candidate, &resetAt)
	require.NoError(t, err)
	require.True(t, applied)
	require.Equal(t, current.PendingBasisByUser, winner.PendingBasisByUser)
	require.InDelta(t, 2.5, winner.PendingUnattributedBasisUSD, 1e-9)
	require.Equal(t, seenAt, winner.SeenThroughCreatedAt)
	require.Equal(t, int64(50), winner.SeenThroughUsageLogID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_ApplyWindowSharesResetsNewWindowAtomically(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	oldWindowStart := time.Date(2026, time.August, 2, 23, 0, 0, 0, time.UTC)
	oldResetAt := oldWindowStart.Add(5 * time.Hour)
	oldObservedAt := oldWindowStart.Add(4 * time.Hour)
	current := service.NewAccountWindowAttributionCheckpoint(80, oldWindowStart, oldObservedAt, &oldResetAt)
	current.UnattributedPercent = 10
	current.PendingBasisByUser = map[int64]float64{1: 4}
	current.SeenThroughCreatedAt = oldObservedAt
	current.SeenThroughUsageLogID = 90
	encoded, err := json.Marshal(current)
	require.NoError(t, err)

	windowStart := oldWindowStart.Add(5 * time.Hour)
	resetAt := windowStart.Add(5 * time.Hour)
	observedAt := windowStart.Add(time.Minute)
	logAt := observedAt
	candidate := service.NewAccountWindowAttributionCheckpoint(4, windowStart, observedAt, &resetAt)
	candidate.UsageLog = &service.AccountWindowUsageLogRef{ID: 100, CreatedAt: logAt, UserID: 1, CostUSD: 1, Inserted: true}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT extra->>\$2,[\s\S]*codex_usage_observed_unix_nano[\s\S]*FOR UPDATE`).
		WithArgs(int64(29), service.AccountWindowAttributionCheckpointExtraKey(service.WindowType5h), service.AccountWindowForceUnattributedExtraKey).
		WillReturnRows(sqlmock.NewRows([]string{"checkpoint", "observed_nano", "observed_at", "force_unattributed"}).
			AddRow(string(encoded), strconv.FormatInt(observedAt.UnixNano(), 10), observedAt.Format(time.RFC3339Nano), false))
	mock.ExpectQuery(`SELECT q.id, q.user_id, q.attributed_percent, q.window_reset_at[\s\S]*FOR UPDATE`).
		WithArgs(int64(29), service.WindowType5h).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "attributed_percent", "window_reset_at"}).
			AddRow(int64(11), int64(1), 40.0, oldResetAt).
			AddRow(int64(12), int64(2), 30.0, oldResetAt))
	mock.ExpectExec(`UPDATE user_account_window_quotas[\s\S]*attributed_percent = 0[\s\S]*donate_pool_fraction = CASE WHEN \$4`).
		WithArgs(int64(29), service.WindowType5h, resetAt, true, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(`UPDATE accounts[\s\S]*extra = COALESCE\(extra`).
		WithArgs(int64(29), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	winner, applied, err := repo.ApplyWindowSharesFromCheckpoint(context.Background(), 29, service.WindowType5h, candidate, &resetAt)
	require.NoError(t, err)
	require.True(t, applied)
	require.InDelta(t, 4, winner.LatestOfficialPercent, 1e-9)
	require.InDelta(t, 4, winner.UnattributedPercent, 1e-9)
	require.Empty(t, winner.PendingBasisByUser)
	require.Zero(t, winner.PendingUnattributedBasisUSD)
	require.Equal(t, logAt, winner.SeenThroughCreatedAt)
	require.Equal(t, int64(100), winner.SeenThroughUsageLogID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_ApplyWindowSharesWithoutBoundaryFreezesExistingHistory(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	observedAt := time.Date(2026, time.August, 3, 7, 0, 0, 0, time.UTC)
	windowStart := observedAt.Add(-5 * time.Hour)
	candidate := service.NewAccountWindowAttributionCheckpoint(60, windowStart, observedAt, nil)
	candidate.UsageLog = &service.AccountWindowUsageLogRef{ID: 100, CreatedAt: observedAt, UserID: 1, CostUSD: 2, Inserted: true}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT extra->>\$2,[\s\S]*codex_usage_observed_unix_nano[\s\S]*FOR UPDATE`).
		WithArgs(int64(29), service.AccountWindowAttributionCheckpointExtraKey(service.WindowType5h), service.AccountWindowForceUnattributedExtraKey).
		WillReturnRows(sqlmock.NewRows([]string{"checkpoint", "observed_nano", "observed_at", "force_unattributed"}).
			AddRow(nil, strconv.FormatInt(observedAt.UnixNano(), 10), observedAt.Format(time.RFC3339Nano), false))
	mock.ExpectQuery(`SELECT q.id, q.user_id, q.attributed_percent, q.window_reset_at[\s\S]*FOR UPDATE`).
		WithArgs(int64(29), service.WindowType5h).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "attributed_percent", "window_reset_at"}).
			AddRow(int64(11), int64(1), 20.0, nil).
			AddRow(int64(12), int64(2), 10.0, nil))
	mock.ExpectExec(`UPDATE accounts[\s\S]*extra = COALESCE\(extra`).
		WithArgs(int64(29), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	winner, applied, err := repo.ApplyWindowSharesFromCheckpoint(context.Background(), 29, service.WindowType5h, candidate, nil)
	require.NoError(t, err)
	require.True(t, applied)
	require.InDelta(t, 60, winner.LatestOfficialPercent, 1e-9)
	require.InDelta(t, 30, winner.UnattributedPercent, 1e-9)
	require.Equal(t, observedAt, winner.SeenThroughCreatedAt)
	require.Equal(t, int64(100), winner.SeenThroughUsageLogID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_ApplyWindowSharesRejectsStaleBoundaryWithoutCheckpoint(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	observedAt := time.Date(2026, time.August, 3, 7, 0, 0, 0, time.UTC)
	resetAt := observedAt.Add(2 * time.Hour)
	newerResetAt := resetAt.Add(2 * time.Hour)
	candidate := service.NewAccountWindowAttributionCheckpoint(60, resetAt.Add(-5*time.Hour), observedAt, &resetAt)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT extra->>\$2,[\s\S]*codex_usage_observed_unix_nano[\s\S]*FOR UPDATE`).
		WithArgs(int64(29), service.AccountWindowAttributionCheckpointExtraKey(service.WindowType5h), service.AccountWindowForceUnattributedExtraKey).
		WillReturnRows(sqlmock.NewRows([]string{"checkpoint", "observed_nano", "observed_at", "force_unattributed"}).
			AddRow(nil, strconv.FormatInt(observedAt.UnixNano(), 10), observedAt.Format(time.RFC3339Nano), false))
	mock.ExpectQuery(`SELECT q.id, q.user_id, q.attributed_percent, q.window_reset_at[\s\S]*FOR UPDATE`).
		WithArgs(int64(29), service.WindowType5h).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "attributed_percent", "window_reset_at"}).
			AddRow(int64(11), int64(1), 4.0, newerResetAt))
	mock.ExpectCommit()

	winner, applied, err := repo.ApplyWindowSharesFromCheckpoint(context.Background(), 29, service.WindowType5h, candidate, &resetAt)
	require.NoError(t, err)
	require.False(t, applied)
	require.Equal(t, candidate, winner)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_ApplyWindowSharesLegacyUpgradeFreezesHistory(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	windowStart := time.Date(2026, time.August, 3, 4, 0, 0, 0, time.UTC)
	resetAt := windowStart.Add(5 * time.Hour)
	observedAt := windowStart.Add(2 * time.Hour)
	legacy := service.NewAccountWindowAttributionCheckpoint(40, windowStart, observedAt.Add(-time.Minute), &resetAt)
	legacy.Basis = "usage_log_total_cost_v3"
	encoded, err := json.Marshal(legacy)
	require.NoError(t, err)
	candidate := service.NewAccountWindowAttributionCheckpoint(60, windowStart, observedAt, &resetAt)
	candidate.UsageLog = &service.AccountWindowUsageLogRef{ID: 100, CreatedAt: observedAt, UserID: 1, CostUSD: 2, Inserted: true}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT extra->>\$2,[\s\S]*codex_usage_observed_unix_nano[\s\S]*FOR UPDATE`).
		WithArgs(int64(29), service.AccountWindowAttributionCheckpointExtraKey(service.WindowType5h), service.AccountWindowForceUnattributedExtraKey).
		WillReturnRows(sqlmock.NewRows([]string{"checkpoint", "observed_nano", "observed_at", "force_unattributed"}).
			AddRow(string(encoded), strconv.FormatInt(observedAt.UnixNano(), 10), observedAt.Format(time.RFC3339Nano), false))
	mock.ExpectQuery(`SELECT q.id, q.user_id, q.attributed_percent, q.window_reset_at[\s\S]*FOR UPDATE`).
		WithArgs(int64(29), service.WindowType5h).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "attributed_percent", "window_reset_at"}).
			AddRow(int64(11), int64(1), 20.0, resetAt).
			AddRow(int64(12), int64(2), 10.0, resetAt))
	mock.ExpectExec(`UPDATE user_account_window_quotas[\s\S]*window_reset_at = \$3`).
		WithArgs(int64(29), service.WindowType5h, resetAt, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(`UPDATE accounts[\s\S]*extra = COALESCE\(extra`).
		WithArgs(int64(29), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	winner, applied, err := repo.ApplyWindowSharesFromCheckpoint(context.Background(), 29, service.WindowType5h, candidate, &resetAt)
	require.NoError(t, err)
	require.True(t, applied)
	require.InDelta(t, 60, winner.LatestOfficialPercent, 1e-9)
	require.InDelta(t, 30, winner.UnattributedPercent, 1e-9)
	require.Empty(t, winner.PendingBasisByUser)
	require.Equal(t, observedAt, winner.SeenThroughCreatedAt)
	require.Equal(t, int64(100), winner.SeenThroughUsageLogID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEffectiveAccountWindowRecomputeCheckpointsChoosesLatestObservation(t *testing.T) {
	now := time.Now().UTC()
	resetAt := now.Add(2 * time.Hour)
	windowStart := resetAt.Add(-5 * time.Hour)

	tests := []struct {
		name               string
		checkpointObserved time.Time
		canonicalObserved  time.Time
		checkpointPercent  float64
		canonicalPercent   float64
		wantPercent        float64
		wantLatestObserved time.Time
	}{
		{
			name:               "canonical snapshot is newer",
			checkpointObserved: now.Add(-10 * time.Minute),
			canonicalObserved:  now.Add(-time.Minute),
			checkpointPercent:  80,
			canonicalPercent:   90,
			wantPercent:        90,
			wantLatestObserved: now.Add(-time.Minute),
		},
		{
			name:               "checkpoint is newer",
			checkpointObserved: now.Add(-time.Minute),
			canonicalObserved:  now.Add(-10 * time.Minute),
			checkpointPercent:  80,
			canonicalPercent:   90,
			wantPercent:        80,
			wantLatestObserved: now.Add(-time.Minute),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkpoint := service.NewAccountWindowAttributionCheckpoint(tt.checkpointPercent, windowStart, tt.checkpointObserved, &resetAt)
			extra := map[string]any{
				"window_quota_shared":            true,
				"codex_5h_used_percent":          tt.canonicalPercent,
				"codex_5h_reset_at":              resetAt.Format(time.RFC3339Nano),
				"codex_usage_updated_at":         tt.canonicalObserved.Format(time.RFC3339Nano),
				"codex_usage_observed_unix_nano": tt.canonicalObserved.UnixNano(),
				service.AccountWindowAttributionCheckpointExtraKey(service.WindowType5h): checkpoint,
			}
			raw, err := json.Marshal(extra)
			require.NoError(t, err)

			checkpoints := effectiveAccountWindowRecomputeCheckpoints(raw, now)
			got, ok := checkpoints[service.WindowType5h]
			require.True(t, ok)
			require.InDelta(t, tt.wantPercent, got.LatestOfficialPercent, 1e-9)
			require.Equal(t, tt.wantLatestObserved, got.LatestObservedAt)
		})
	}
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

func TestUserAccountWindowQuotaRepository_RecomputeFullWindowUsesCostSharesAndAccountScope(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	windowStart := time.Date(2026, 7, 11, 11, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT q\\.id, q\\.user_id").
		WithArgs(int64(29), service.WindowType5h).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "window_reset_at"}).
			AddRow(101, 1, nil).
			AddRow(102, 31, nil).
			AddRow(103, 32, nil).
			AddRow(104, 33, nil))
	mock.ExpectQuery("SELECT user_id,\\s+COALESCE\\(SUM\\(GREATEST\\(COALESCE\\(total_cost, 0\\), 0\\)\\), 0\\)::double precision\\s+FROM usage_logs\\s+WHERE account_id = \\$1 AND created_at >= \\$2\\s+AND \\(\\$3::timestamptz IS NULL OR created_at < \\$3\\)").
		WithArgs(int64(29), windowStart, nil, int64(1), int64(31), int64(32), int64(33)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "cost"}).
			AddRow(1, 3.0).
			AddRow(31, 1.0))
	for i, id := range []int64{101, 102, 103, 104} {
		values := []float64{22.5, 7.5, 0, 0}
		mock.ExpectExec("UPDATE user_account_window_quotas\\s+SET attributed_percent").
			WithArgs(id, values[i], nil, sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()

	err := repo.RecomputeWindowSharesFromCheckpoint(context.Background(), 29, service.WindowType5h, 30, windowStart, nil)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_RecomputeFromNewWindowUsesFullWindowCostShares(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	windowStart := time.Date(2026, 7, 26, 3, 45, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT q\\.id, q\\.user_id").
		WithArgs(int64(29), service.WindowType7d).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "window_reset_at"}).
			AddRow(101, 1, nil).
			AddRow(102, 31, nil).
			AddRow(103, 32, nil).
			AddRow(104, 33, nil))
	mock.ExpectQuery("SELECT user_id,\\s+COALESCE\\(SUM\\(GREATEST\\(COALESCE\\(total_cost, 0\\), 0\\)\\), 0\\)::double precision\\s+FROM usage_logs\\s+WHERE account_id = \\$1 AND created_at >= \\$2\\s+AND \\(\\$3::timestamptz IS NULL OR created_at < \\$3\\)").
		WithArgs(int64(29), windowStart, nil, int64(1), int64(31), int64(32), int64(33)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "cost"}).
			AddRow(1, 6.0).
			AddRow(32, 4.0))
	for i, id := range []int64{101, 102, 103, 104} {
		values := []float64{2.4, 0, 1.6, 0}
		mock.ExpectExec("UPDATE user_account_window_quotas\\s+SET attributed_percent").
			WithArgs(id, values[i], nil, sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()

	err := repo.RecomputeWindowSharesFromCheckpoint(context.Background(), 29, service.WindowType7d, 4, windowStart, nil)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_RecomputeFullWindowIgnoresPriorAttributedShares(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	windowStart := time.Date(2026, 7, 11, 11, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT q\\.id, q\\.user_id").
		WithArgs(int64(29), service.WindowType7d).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "window_reset_at"}).
			AddRow(101, 1, nil).
			AddRow(102, 31, nil).
			AddRow(103, 32, nil).
			AddRow(104, 33, nil))
	mock.ExpectQuery("SELECT user_id,\\s+COALESCE\\(SUM\\(GREATEST\\(COALESCE\\(total_cost, 0\\), 0\\)\\), 0\\)::double precision\\s+FROM usage_logs\\s+WHERE account_id = \\$1 AND created_at >= \\$2\\s+AND \\(\\$3::timestamptz IS NULL OR created_at < \\$3\\)").
		WithArgs(int64(29), windowStart, nil, int64(1), int64(31), int64(32), int64(33)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "cost"}).AddRow(1, 100.0))
	for i, id := range []int64{101, 102, 103, 104} {
		values := []float64{4, 0, 0, 0}
		mock.ExpectExec("UPDATE user_account_window_quotas\\s+SET attributed_percent").
			WithArgs(id, values[i], nil, sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()

	err := repo.RecomputeWindowSharesFromCheckpoint(context.Background(), 29, service.WindowType7d, 4, windowStart, nil)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_DelayedOfficialIncreaseWithoutNewRequestUsesFullWindowCostShares(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	windowStart := time.Date(2026, 8, 2, 2, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT q\\.id, q\\.user_id").
		WithArgs(int64(29), service.WindowType5h).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "window_reset_at"}).
			AddRow(101, 1, nil).
			AddRow(102, 31, nil))
	mock.ExpectQuery("SELECT user_id,\\s+COALESCE\\(SUM\\(GREATEST\\(COALESCE\\(total_cost, 0\\), 0\\)\\), 0\\)::double precision\\s+FROM usage_logs\\s+WHERE account_id = \\$1 AND created_at >= \\$2\\s+AND \\(\\$3::timestamptz IS NULL OR created_at < \\$3\\)").
		WithArgs(int64(29), windowStart, nil, int64(1), int64(31)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "cost"}).
			AddRow(1, 3.0).
			AddRow(31, 1.0))
	for i, id := range []int64{101, 102} {
		values := []float64{27, 9}
		mock.ExpectExec("UPDATE user_account_window_quotas\\s+SET attributed_percent").
			WithArgs(id, values[i], nil, sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()

	err := repo.RecomputeWindowSharesFromCheckpoint(context.Background(), 29, service.WindowType5h, 36, windowStart, nil)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_CostSharesConserveOfficialPercentAtFourDecimals(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	windowStart := time.Date(2026, 8, 1, 4, 6, 26, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT q\\.id, q\\.user_id").
		WithArgs(int64(29), service.WindowType7d).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "window_reset_at"}).
			AddRow(101, 1, nil).
			AddRow(102, 31, nil).
			AddRow(103, 32, nil))
	mock.ExpectQuery("SELECT user_id,\\s+COALESCE\\(SUM\\(GREATEST\\(COALESCE\\(total_cost, 0\\), 0\\)\\), 0\\)::double precision\\s+FROM usage_logs\\s+WHERE account_id = \\$1 AND created_at >= \\$2\\s+AND \\(\\$3::timestamptz IS NULL OR created_at < \\$3\\)").
		WithArgs(int64(29), windowStart, nil, int64(1), int64(31), int64(32)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "cost"}).
			AddRow(1, 1.0).
			AddRow(31, 1.0).
			AddRow(32, 1.0))
	for i, id := range []int64{101, 102, 103} {
		values := []float64{25.3333, 25.3333, 25.3334}
		mock.ExpectExec("UPDATE user_account_window_quotas\\s+SET attributed_percent").
			WithArgs(id, values[i], nil, sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()

	err := repo.RecomputeWindowSharesFromCheckpoint(context.Background(), 29, service.WindowType7d, 76, windowStart, nil)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_RoundingRemainderNeverGoesToZeroCostMember(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	windowStart := time.Date(2026, 8, 2, 4, 6, 26, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT q\\.id, q\\.user_id").
		WithArgs(int64(29), service.WindowType5h).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "window_reset_at"}).
			AddRow(101, 1, nil).
			AddRow(102, 31, nil).
			AddRow(103, 32, nil))
	mock.ExpectQuery("SELECT user_id,\\s+COALESCE\\(SUM\\(GREATEST\\(COALESCE\\(total_cost, 0\\), 0\\)\\), 0\\)::double precision\\s+FROM usage_logs\\s+WHERE account_id = \\$1 AND created_at >= \\$2\\s+AND \\(\\$3::timestamptz IS NULL OR created_at < \\$3\\)").
		WithArgs(int64(29), windowStart, nil, int64(1), int64(31), int64(32)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "cost"}).
			AddRow(1, 1.0).
			AddRow(31, 2.0))
	for i, id := range []int64{101, 102, 103} {
		values := []float64{0.3333, 0.6667, 0}
		mock.ExpectExec("UPDATE user_account_window_quotas\\s+SET attributed_percent").
			WithArgs(id, values[i], nil, sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()

	err := repo.RecomputeWindowSharesFromCheckpoint(context.Background(), 29, service.WindowType5h, 1, windowStart, nil)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_ZeroCostDoesNotFallBackToTokens(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	windowStart := time.Date(2026, 8, 1, 4, 6, 26, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT q\\.id, q\\.user_id").
		WithArgs(int64(29), service.WindowType7d).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "window_reset_at"}).
			AddRow(101, 1, nil).
			AddRow(102, 31, nil))
	mock.ExpectQuery("SELECT user_id,\\s+COALESCE\\(SUM\\(GREATEST\\(COALESCE\\(total_cost, 0\\), 0\\)\\), 0\\)::double precision\\s+FROM usage_logs\\s+WHERE account_id = \\$1 AND created_at >= \\$2\\s+AND \\(\\$3::timestamptz IS NULL OR created_at < \\$3\\)").
		WithArgs(int64(29), windowStart, nil, int64(1), int64(31)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "cost"}))
	for _, id := range []int64{101, 102} {
		mock.ExpectExec("UPDATE user_account_window_quotas\\s+SET attributed_percent").
			WithArgs(id, float64(0), nil, sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()

	err := repo.RecomputeWindowSharesFromCheckpoint(context.Background(), 29, service.WindowType7d, 4, windowStart, nil)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_StaleResetBoundaryCannotOverwriteNewWindow(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	windowStart := time.Date(2026, 7, 25, 4, 6, 26, 0, time.UTC)
	incomingResetAt := time.Date(2026, 8, 1, 4, 6, 26, 0, time.UTC)
	currentResetAt := time.Date(2026, 8, 8, 4, 6, 26, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT q\\.id, q\\.user_id").
		WithArgs(int64(29), service.WindowType7d).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "window_reset_at"}).
			AddRow(101, 1, currentResetAt))
	mock.ExpectRollback()

	err := repo.RecomputeWindowSharesFromCheckpoint(context.Background(), 29, service.WindowType7d, 80, windowStart, &incomingResetAt)
	require.ErrorIs(t, err, errAccountWindowStaleResetBoundary)
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

func TestUserAccountWindowQuotaRepository_GetAccountOfficialWindowPercent(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	resetAt := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	mock.ExpectQuery("SELECT[\\s\\S]*extra->>\\$2[\\s\\S]*codex_usage_updated_at[\\s\\S]*FROM accounts").
		WithArgs(int64(29), "codex_5h_used_percent", "codex_5h_reset_at", "codex_5h_reset", "codex_5h_reset_after_seconds", service.AccountWindowAttributionCheckpointExtraKey(service.WindowType5h)).
		WillReturnRows(sqlmock.NewRows([]string{"used", "reset_at", "legacy_reset", "reset_after", "observed_at", "observed_nano", "checkpoint"}).
			AddRow("95.25", resetAt, nil, nil, nil, nil, nil))

	value, found, err := repo.GetAccountOfficialWindowPercent(context.Background(), 29, service.WindowType5h)
	require.NoError(t, err)
	require.True(t, found)
	require.InDelta(t, 95.25, value, 1e-9)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_GetAccountOfficialWindowPercentSelectsMonotonicLatestWindow(t *testing.T) {
	now := time.Now().UTC()
	checkpointResetAt := now.Add(2 * time.Hour)

	tests := []struct {
		name               string
		checkpointObserved time.Time
		canonicalObserved  time.Time
		canonicalResetAt   time.Time
		checkpointPercent  float64
		canonicalPercent   float64
		wantPercent        float64
	}{
		{
			name:               "newer lower jitter cannot decrease same window",
			checkpointObserved: now.Add(-10 * time.Minute),
			canonicalObserved:  now.Add(-time.Minute),
			canonicalResetAt:   checkpointResetAt,
			checkpointPercent:  80,
			canonicalPercent:   70,
			wantPercent:        80,
		},
		{
			name:               "newer higher snapshot advances same window",
			checkpointObserved: now.Add(-10 * time.Minute),
			canonicalObserved:  now.Add(-time.Minute),
			canonicalResetAt:   checkpointResetAt,
			checkpointPercent:  80,
			canonicalPercent:   90,
			wantPercent:        90,
		},
		{
			name:               "new official window may restart lower",
			checkpointObserved: now.Add(-10 * time.Minute),
			canonicalObserved:  now.Add(-time.Minute),
			canonicalResetAt:   checkpointResetAt.Add(5 * time.Hour),
			checkpointPercent:  80,
			canonicalPercent:   10,
			wantPercent:        10,
		},
		{
			name:               "older canonical snapshot cannot replace checkpoint",
			checkpointObserved: now.Add(-time.Minute),
			canonicalObserved:  now.Add(-10 * time.Minute),
			canonicalResetAt:   checkpointResetAt,
			checkpointPercent:  80,
			canonicalPercent:   90,
			wantPercent:        80,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, mock := newWindowQuotaRepoSQLMock(t)
			checkpoint := service.NewAccountWindowAttributionCheckpoint(
				tt.checkpointPercent,
				checkpointResetAt.Add(-5*time.Hour),
				tt.checkpointObserved,
				&checkpointResetAt,
			)
			encoded, err := json.Marshal(checkpoint)
			require.NoError(t, err)
			mock.ExpectQuery("SELECT[\\s\\S]*extra->>\\$2[\\s\\S]*codex_usage_updated_at[\\s\\S]*FROM accounts").
				WithArgs(int64(29), "codex_5h_used_percent", "codex_5h_reset_at", "codex_5h_reset", "codex_5h_reset_after_seconds", service.AccountWindowAttributionCheckpointExtraKey(service.WindowType5h)).
				WillReturnRows(sqlmock.NewRows([]string{"used", "reset_at", "legacy_reset", "reset_after", "observed_at", "observed_nano", "checkpoint"}).
					AddRow(
						strconv.FormatFloat(tt.canonicalPercent, 'f', -1, 64),
						tt.canonicalResetAt.Format(time.RFC3339Nano),
						nil,
						nil,
						tt.canonicalObserved.Format(time.RFC3339Nano),
						strconv.FormatInt(tt.canonicalObserved.UnixNano(), 10),
						string(encoded),
					))

			value, found, err := repo.GetAccountOfficialWindowPercent(context.Background(), 29, service.WindowType5h)
			require.NoError(t, err)
			require.True(t, found)
			require.InDelta(t, tt.wantPercent, value, 1e-9)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestUserAccountWindowQuotaRepository_GetAccountOfficialWindowPercentSupportsLegacyUnixReset(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	resetAt := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	mock.ExpectQuery("SELECT[\\s\\S]*extra->>\\$2[\\s\\S]*codex_usage_updated_at[\\s\\S]*FROM accounts").
		WithArgs(int64(29), "codex_5h_used_percent", "codex_5h_reset_at", "codex_5h_reset", "codex_5h_reset_after_seconds", service.AccountWindowAttributionCheckpointExtraKey(service.WindowType5h)).
		WillReturnRows(sqlmock.NewRows([]string{"used", "reset_at", "legacy_reset", "reset_after", "observed_at", "observed_nano", "checkpoint"}).
			AddRow("42", nil, resetAt, nil, nil, nil, nil))

	value, found, err := repo.GetAccountOfficialWindowPercent(context.Background(), 29, service.WindowType5h)
	require.NoError(t, err)
	require.True(t, found)
	require.InDelta(t, 42, value, 1e-9)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_GetAccountOfficialWindowPercentRejectsExpiredSnapshot(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	resetAt := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	mock.ExpectQuery("SELECT[\\s\\S]*extra->>\\$2[\\s\\S]*codex_usage_updated_at[\\s\\S]*FROM accounts").
		WithArgs(int64(50), "codex_7d_used_percent", "codex_7d_reset_at", "codex_7d_reset", "codex_7d_reset_after_seconds", service.AccountWindowAttributionCheckpointExtraKey(service.WindowType7d)).
		WillReturnRows(sqlmock.NewRows([]string{"used", "reset_at", "legacy_reset", "reset_after", "observed_at", "observed_nano", "checkpoint"}).
			AddRow("99", resetAt, nil, nil, nil, nil, nil))

	value, found, err := repo.GetAccountOfficialWindowPercent(context.Background(), 50, service.WindowType7d)
	require.NoError(t, err)
	require.False(t, found)
	require.Zero(t, value)
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

func TestUserAccountWindowQuotaRepository_SyncAccountMembers_PreservesRetainedStateAndUsesSafeDefaults(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	baseNow := time.Now().UTC()
	reset5h := baseNow.Add(6 * time.Hour)
	reset7d := baseNow.Add(6 * 24 * time.Hour)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT COALESCE\\(extra, '\\{\\}'::jsonb\\)::text[\\s\\S]*FROM accounts[\\s\\S]*FOR UPDATE").
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"extra"}).AddRow(`{"window_quota_shared":true}`))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM users").
		WithArgs(int64(1), int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(`SELECT user_id, window_type, limit_percent, attributed_percent, window_reset_at[\s\S]*FOR UPDATE`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "window_type", "limit_percent", "attributed_percent", "window_reset_at"}).
			AddRow(int64(1), service.WindowType5h, 70.0, 30.0, reset5h).
			AddRow(int64(1), service.WindowType7d, 60.0, 20.0, reset7d).
			AddRow(int64(3), service.WindowType5h, 22.0, 12.0, reset5h).
			AddRow(int64(3), service.WindowType7d, 22.0, 9.0, reset7d))
	mock.ExpectExec("UPDATE user_account_window_quotas SET deleted_at").
		WithArgs(int64(7), sqlmock.AnyArg(), int64(1), int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("INSERT INTO user_account_window_quotas").
		WithArgs(int64(2), int64(7), service.WindowType5h, 22.0, reset5h, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO user_account_window_quotas").
		WithArgs(int64(2), int64(7), service.WindowType7d, 32.0, reset7d, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	results, err := repo.SyncAccountMembers(context.Background(), 7, []int64{1, 2}, 92, 92)
	require.NoError(t, err)
	require.Equal(t, []service.AccountWindowEqualizationResult{
		{WindowType: service.WindowType5h, ActiveMemberCount: 2, SharePercent: 22, CeilingPercent: 92},
		{WindowType: service.WindowType7d, ActiveMemberCount: 2, SharePercent: 32, CeilingPercent: 92},
	}, results)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserAccountWindowQuotaRepository_SyncAccountMembersBaselinesNewMemberHistory(t *testing.T) {
	repo, mock := newWindowQuotaRepoSQLMock(t)
	baseNow := time.Now().UTC()
	windowStart := baseNow.Add(-2 * time.Hour)
	reset5h := baseNow.Add(3 * time.Hour)
	reset7d := baseNow.Add(6 * 24 * time.Hour)
	seenAt := baseNow.Add(-30 * time.Minute)
	upperAt := baseNow.Add(-10 * time.Minute)
	checkpoint := service.NewAccountWindowAttributionCheckpoint(40, windowStart, baseNow.Add(-time.Minute), &reset5h)
	checkpoint.UnattributedPercent = 5
	checkpoint.PendingBasisByUser = map[int64]float64{3: 2}
	checkpoint.SeenThroughCreatedAt = seenAt
	checkpoint.SeenThroughUsageLogID = 50
	extra, err := json.Marshal(map[string]any{
		"window_quota_shared": true,
		service.AccountWindowAttributionCheckpointExtraKey(service.WindowType5h): checkpoint,
	})
	require.NoError(t, err)

	var checkpointPayload string
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT COALESCE\\(extra, '\\{\\}'::jsonb\\)::text[\\s\\S]*FROM accounts[\\s\\S]*FOR UPDATE").
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"extra"}).AddRow(string(extra)))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM users").
		WithArgs(int64(1), int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(`SELECT user_id, window_type, limit_percent, attributed_percent, window_reset_at[\s\S]*FOR UPDATE`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "window_type", "limit_percent", "attributed_percent", "window_reset_at"}).
			AddRow(int64(1), service.WindowType5h, 70.0, 20.0, reset5h).
			AddRow(int64(1), service.WindowType7d, 60.0, 10.0, reset7d).
			AddRow(int64(3), service.WindowType5h, 22.0, 7.0, reset5h).
			AddRow(int64(3), service.WindowType7d, 22.0, 4.0, reset7d))
	mock.ExpectQuery(`SELECT created_at, id[\s\S]*ORDER BY created_at DESC`).
		WithArgs(int64(7), windowStart, reset5h, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"created_at", "id"}).AddRow(upperAt, int64(100)))
	mock.ExpectQuery(`SELECT user_id,[\s\S]*SUM\(GREATEST[\s\S]*\(created_at, id\) >`).
		WithArgs(int64(7), windowStart, reset5h, seenAt, int64(50), upperAt, int64(100)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "cost"}).
			AddRow(int64(1), 3.0).
			AddRow(int64(2), 4.0).
			AddRow(int64(3), 5.0))
	mock.ExpectExec(`UPDATE accounts[\s\S]*extra = COALESCE\(extra`).
		WithArgs(int64(7), captureStringArgument{value: &checkpointPayload}).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE user_account_window_quotas SET deleted_at").
		WithArgs(int64(7), sqlmock.AnyArg(), int64(1), int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("INSERT INTO user_account_window_quotas").
		WithArgs(int64(2), int64(7), service.WindowType5h, 22.0, reset5h, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO user_account_window_quotas").
		WithArgs(int64(2), int64(7), service.WindowType7d, 32.0, reset7d, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	results, err := repo.SyncAccountMembers(context.Background(), 7, []int64{1, 2}, 92, 92)
	require.NoError(t, err)
	require.Equal(t, []service.AccountWindowEqualizationResult{
		{WindowType: service.WindowType5h, ActiveMemberCount: 2, SharePercent: 22, CeilingPercent: 92},
		{WindowType: service.WindowType7d, ActiveMemberCount: 2, SharePercent: 32, CeilingPercent: 92},
	}, results)

	var payload map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(checkpointPayload), &payload))
	var persisted service.AccountWindowAttributionCheckpoint
	require.NoError(t, json.Unmarshal(payload[service.AccountWindowAttributionCheckpointExtraKey(service.WindowType5h)], &persisted))
	require.Equal(t, map[int64]float64{1: 3}, persisted.PendingBasisByUser)
	require.InDelta(t, 11, persisted.PendingUnattributedBasisUSD, 1e-9)
	require.InDelta(t, 12, persisted.UnattributedPercent, 1e-9)
	require.Equal(t, upperAt, persisted.SeenThroughCreatedAt)
	require.Equal(t, int64(100), persisted.SeenThroughUsageLogID)
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
