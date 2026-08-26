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

// ErrUserAccountWindowQuotaExceeded 表示发起用户在该账号的某官方窗口占用已达上限。
var ErrUserAccountWindowQuotaExceeded = errors.New("user account window quota exceeded")

// UserAccountWindowQuotaExceededError 保留被阻断窗口及重置时间，供调度器换号，
// 并仅在候选池耗尽后生成最终 429。
type UserAccountWindowQuotaExceededError struct {
	Window  string
	ResetAt *time.Time
}

func (e *UserAccountWindowQuotaExceededError) Error() string {
	if e == nil {
		return ErrUserAccountWindowQuotaExceeded.Error()
	}
	return fmt.Sprintf("%s: window=%s", ErrUserAccountWindowQuotaExceeded.Error(), e.Window)
}

func (e *UserAccountWindowQuotaExceededError) Unwrap() error {
	return ErrUserAccountWindowQuotaExceeded
}

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

// CheckAccountWindowQuota 是无响应副作用的账号资格检查。处理器可先排除受限账号继续选号，
// 各转发入口也调用它作为竞态兜底。
func (s *OpenAIGatewayService) CheckAccountWindowQuota(ctx context.Context, c *gin.Context, account *Account) error {
	if c == nil || account == nil || !account.IsWindowQuotaShared() || s.accountWindowQuota == nil || !s.accountWindowQuota.Enabled() {
		return nil
	}
	userID := userIDFromGinContext(c)
	if userID <= 0 {
		return nil
	}
	eligible, window, resetAt := s.accountWindowQuota.CheckUserAccountEligible(ctx, userID, account.ID)
	if eligible {
		return nil
	}
	return &UserAccountWindowQuotaExceededError{Window: window, ResetAt: resetAt}
}

// WriteAccountWindowQuotaExceeded 写出候选池耗尽后的最终 429。
func WriteAccountWindowQuotaExceeded(c *gin.Context, quotaErr error) {
	if c == nil {
		return
	}
	window := WindowType5h
	var resetAt *time.Time
	var detail *UserAccountWindowQuotaExceededError
	if errors.As(quotaErr, &detail) && detail != nil {
		if detail.Window != "" {
			window = detail.Window
		}
		resetAt = detail.ResetAt
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
}
