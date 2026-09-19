package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestOpsModelMismatchGroups(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	r := &opsRepository{db: db}
	end := time.Now()
	start := end.Add(-5 * time.Minute)
	groupID := int64(9)
	columns := []string{"account_id", "requested_model", "sent_model", "response_model", "occurrences", "total"}
	mock.ExpectQuery(regexp.QuoteMeta(opsModelMismatchGroupsSQL)).WithArgs(start, end, "openai", groupID).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(51, "gpt-6-astra", "gpt-6-astra", "gpt-5.6-luna", 3, 25)).RowsWillBeClosed()
	groups, err := r.GetModelMismatchGroups(context.Background(), start, end, "openai", &groupID)
	require.NoError(t, err)
	require.Len(t, groups, 1)
	require.EqualValues(t, 25, groups[0].Total)
	require.EqualValues(t, 3, groups[0].Count)
	mock.ExpectQuery(regexp.QuoteMeta(opsModelMismatchGroupsSQL)).WithArgs(start, end, "", nil).
		WillReturnError(errors.New("timeout"))
	_, err = r.GetModelMismatchGroups(context.Background(), start, end, "", nil)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
