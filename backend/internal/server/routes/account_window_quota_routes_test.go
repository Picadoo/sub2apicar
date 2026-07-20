package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newAccountWindowQuotaRoutesTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	v1 := router.Group("/api/v1")
	h := &handler.Handlers{AccountWindowQuota: handler.NewAccountWindowQuotaHandler(nil)}

	RegisterUserRoutes(v1, h, servermiddleware.JWTAuthMiddleware(func(c *gin.Context) {
		c.Next()
	}), nil)

	admin := v1.Group("/admin")
	admin.Use(func(c *gin.Context) {
		if c.GetHeader("X-Test-Admin") != "yes" {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Next()
	})
	registerAccountWindowQuotaRoutes(admin, h)
	return router
}

func TestAccountWindowQuotaRoutes_OverviewIsAdminOnlyAndEqualizeIsRegistered(t *testing.T) {
	router := newAccountWindowQuotaRoutesTestRouter()
	routes := router.Routes()
	registered := make(map[string]bool, len(routes))
	for _, route := range routes {
		registered[route.Method+" "+route.Path] = true
	}

	require.False(t, registered[http.MethodGet+" /api/v1/user/account-window-quotas/overview"])
	require.True(t, registered[http.MethodGet+" /api/v1/admin/account-window-quotas/overview"])
	require.True(t, registered[http.MethodPost+" /api/v1/admin/account-window-quotas/accounts/:id/equalize"])
	require.True(t, registered[http.MethodPut+" /api/v1/admin/account-window-quotas/accounts/:id/members"])

	userReq := httptest.NewRequest(http.MethodGet, "/api/v1/user/account-window-quotas/overview", nil)
	userW := httptest.NewRecorder()
	router.ServeHTTP(userW, userReq)
	require.Equal(t, http.StatusNotFound, userW.Code)

	adminReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/account-window-quotas/overview", nil)
	adminW := httptest.NewRecorder()
	router.ServeHTTP(adminW, adminReq)
	require.Equal(t, http.StatusForbidden, adminW.Code)

	adminAuthorizedReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/account-window-quotas/overview", nil)
	adminAuthorizedReq.Header.Set("X-Test-Admin", "yes")
	adminAuthorizedW := httptest.NewRecorder()
	router.ServeHTTP(adminAuthorizedW, adminAuthorizedReq)
	require.Equal(t, http.StatusOK, adminAuthorizedW.Code)
}
