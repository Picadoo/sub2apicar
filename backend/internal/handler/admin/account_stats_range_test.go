package admin

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type accountStatsRangeRepo struct {
	service.UsageLogRepository
	start, end time.Time
	accountID  int64
}

func (r *accountStatsRangeRepo) GetAccountUsageStats(_ context.Context, id int64, start, end time.Time) (*usagestats.AccountUsageStatsResponse, error) {
	r.start, r.end, r.accountID = start, end, id
	return &usagestats.AccountUsageStatsResponse{}, nil
}
func TestAccountStatsAllTimeAndRollingRange(t *testing.T) {
	for _, query := range []string{"all", "30", "90", "", "invalid"} {
		t.Run(query, func(t *testing.T) {
			repo := &accountStatsRangeRepo{}
			svc := service.NewAccountUsageService(nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			h := &AccountHandler{accountUsageService: svc}
			router := gin.New()
			router.GET("/accounts/:id/stats", h.GetStats)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/accounts/29/stats?days="+query, nil))
			require.Equal(t, http.StatusOK, w.Code)
			require.Equal(t, int64(29), repo.accountID)
			require.False(t, repo.end.IsZero())
			if query == "all" {
				require.True(t, repo.start.IsZero())
			} else {
				days := 30
				if query == "90" {
					days = 90
				}
				require.Equal(t, repo.end.AddDate(0, 0, -days), repo.start)
			}
		})
	}
}
