package repository

import (
	"context"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestAccountUsageStatsAllRetainedHistory(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "older than 90 days", true: "empty account"}[empty], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			repo := newUsageLogRepositoryWithSQL(nil, db)
			end := timezone.StartOfDay(time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC))
			start := end.AddDate(0, 0, -150)
			first := sqlmock.NewRows([]string{"min"})
			if empty {
				first.AddRow(nil)
				start = end.AddDate(0, 0, -1)
			} else {
				first.AddRow(start.Add(time.Hour))
			}
			mock.ExpectQuery(`SELECT MIN\(created_at\) FROM usage_logs WHERE account_id = \$1 AND created_at < \$2`).WithArgs(int64(29), end).WillReturnRows(first)
			history := sqlmock.NewRows([]string{"date", "requests", "tokens", "cost", "actual_cost", "user_cost"})
			if !empty {
				history.AddRow(start.Format("2006-01-02"), 12001, 200, 2.0, 1.0, 3.0).AddRow("2026-09-25", 2, 100, 1.0, 0.5, 1.5)
			}
			mock.ExpectQuery(`(?s)SELECT.*TO_CHAR.*WHERE account_id = \$1 AND created_at >= \$2 AND created_at < \$3`).WithArgs(int64(29), start, end).WillReturnRows(history)
			mock.ExpectQuery(`SELECT COALESCE\(AVG`).WithArgs(int64(29), start, end).WillReturnRows(sqlmock.NewRows([]string{"avg"}).AddRow(0))
			mock.ExpectQuery(`(?s)SELECT.*as model,.*AND account_id = \$3`).WithArgs(start, end, int64(29)).WillReturnRows(sqlmock.NewRows([]string{"model"}))
			for _, column := range []string{"inbound_endpoint", "upstream_endpoint"} {
				mock.ExpectQuery(`(?s)SELECT.*TRIM\(`+column+`\).*AND account_id = \$3`).WithArgs(start, end, int64(29)).WillReturnRows(sqlmock.NewRows([]string{"endpoint"}))
			}
			stats, err := repo.GetAccountUsageStats(context.Background(), 29, time.Time{}, end)
			require.NoError(t, err)
			if empty {
				require.Zero(t, stats.Summary.TotalRequests)
			} else {
				require.Equal(t, int64(12003), stats.Summary.TotalRequests)
				require.Equal(t, 2, stats.Summary.ActualDaysUsed)
				require.InDelta(t, 1.5, stats.Summary.TotalCost, 1e-9)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
