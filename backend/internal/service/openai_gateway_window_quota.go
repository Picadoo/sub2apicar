package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// ErrUserAccountWindowQuotaExceeded 表示发起用户在该账号的某官方窗口占用已达上限，
// 转发前预检直接拒绝。enforceAccountWindowQuota 命中时已写好 429 响应，调用方/处理器
// 不应再写任何响应，只需结束请求。
var ErrUserAccountWindowQuotaExceeded = errors.New("user account window quota exceeded")

// userIDFromGinContext 从 gin 上下文取出鉴权用户 ID（供配额分摊使用）。
// 读取 ApiKeyAuth 中间件写入的 "api_key"（*APIKey）；缺失或类型不符时返回 0。
// 直接读裸键避免 service → middleware 的导入环（middleware 依赖 service）。
func userIDFromGinContext(c *gin.Context) int64 {
	if c == nil {
		return 0
	}
	if v, ok := c.Get("api_key"); ok {
		if apiKey, ok := v.(*APIKey); ok && apiKey != nil {
			return apiKey.UserID
		}
	}
	return 0
}

// enforceAccountWindowQuota 在转发前校验发起用户在该账号官方窗口（5h/7d）的占用是否已达上限。
// 命中上限时直接写 429（含被打满窗口、重置时间与 Retry-After）并返回 true（调用方应中止转发）。
// fail-open：服务未启用、无用户身份或底层异常时返回 false（放行）。
func (s *OpenAIGatewayService) enforceAccountWindowQuota(ctx context.Context, c *gin.Context, account *Account) bool {
	if c == nil || account == nil || !account.IsWindowQuotaShared() || s.accountWindowQuota == nil || !s.accountWindowQuota.Enabled() {
		return false
	}
	userID := userIDFromGinContext(c)
	if userID <= 0 {
		return false
	}
	eligible, window, resetAt := s.accountWindowQuota.CheckUserAccountEligible(ctx, userID, account.ID)
	if eligible {
		return false
	}

	MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonWindowQuotaExceeded)

	errBody := gin.H{
		"type":    "rate_limit_error",
		"code":    "user_account_window_quota_exceeded",
		"message": fmt.Sprintf("You have reached your %s window usage limit on this account; access resumes after the official window resets.", window),
		"window":  window,
	}
	if resetAt != nil {
		retryAfter := int(time.Until(*resetAt).Seconds())
		if retryAfter < 1 {
			retryAfter = 1
		}
		c.Header("Retry-After", strconv.Itoa(retryAfter))
		errBody["reset_at"] = resetAt.UTC().Format(time.RFC3339)
	}
	c.JSON(http.StatusTooManyRequests, gin.H{"error": errBody})
	return true
}
