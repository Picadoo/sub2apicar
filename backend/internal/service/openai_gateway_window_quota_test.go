package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newWindowQuotaBlockedGatewayTestContext(t *testing.T) (*OpenAIGatewayService, *gin.Context, *httptest.ResponseRecorder, *Account) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set("api_key", &APIKey{UserID: 99})
	quota := newStubQuotaService(t, nil)
	return &OpenAIGatewayService{accountWindowQuota: quota}, c, recorder, &Account{
		ID:       29,
		Type:     AccountTypeOAuth,
		Platform: PlatformOpenAI,
		Extra:    map[string]any{AccountExtraWindowQuotaShared: true},
	}
}

func TestOpenAIGatewayWindowQuota_AllHTTPForwardEntrypointsFailBeforeUpstream(t *testing.T) {
	tests := []struct {
		name string
		call func(*OpenAIGatewayService, *gin.Context, *Account) error
	}{
		{name: "responses", call: func(s *OpenAIGatewayService, c *gin.Context, account *Account) error {
			_, err := s.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5"}`))
			return err
		}},
		{name: "chat completions", call: func(s *OpenAIGatewayService, c *gin.Context, account *Account) error {
			_, err := s.ForwardAsChatCompletions(context.Background(), c, account, []byte(`{"model":"gpt-5"}`), "", "")
			return err
		}},
		{name: "messages", call: func(s *OpenAIGatewayService, c *gin.Context, account *Account) error {
			_, err := s.ForwardAsAnthropic(context.Background(), c, account, []byte(`{"model":"gpt-5"}`), "", "")
			return err
		}},
		{name: "alpha search", call: func(s *OpenAIGatewayService, c *gin.Context, account *Account) error {
			_, err := s.ForwardAlphaSearch(context.Background(), c, account, []byte(`{"model":"gpt-5"}`))
			return err
		}},
		{name: "embeddings", call: func(s *OpenAIGatewayService, c *gin.Context, account *Account) error {
			_, err := s.ForwardEmbeddings(context.Background(), c, account, []byte(`{"model":"text-embedding-3-small"}`), "")
			return err
		}},
		{name: "oauth images", call: func(s *OpenAIGatewayService, c *gin.Context, account *Account) error {
			_, err := s.ForwardImages(context.Background(), c, account, nil, &OpenAIImagesRequest{}, "")
			return err
		}},
		{name: "oauth images responses", call: func(s *OpenAIGatewayService, c *gin.Context, account *Account) error {
			_, err := s.forwardOpenAIImagesOAuth(context.Background(), c, account, &OpenAIImagesRequest{}, "")
			return err
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, c, recorder, account := newWindowQuotaBlockedGatewayTestContext(t)
			err := tt.call(svc, c, account)
			require.ErrorIs(t, err, ErrUserAccountWindowQuotaExceeded)
			require.Equal(t, 200, recorder.Code, "资格检查不能提前写响应，调度器需要继续换号")
			require.Empty(t, recorder.Body.String())
		})
	}
}

func TestWriteAccountWindowQuotaExceeded_WritesFinal429(t *testing.T) {
	_, c, recorder, _ := newWindowQuotaBlockedGatewayTestContext(t)
	quotaErr := &UserAccountWindowQuotaExceededError{Window: WindowType7d}
	require.True(t, errors.Is(quotaErr, ErrUserAccountWindowQuotaExceeded))

	WriteAccountWindowQuotaExceeded(c, quotaErr)

	require.Equal(t, http.StatusTooManyRequests, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"window":"7d"`)
	require.Contains(t, recorder.Body.String(), `"code":"user_account_window_quota_exceeded"`)
}
