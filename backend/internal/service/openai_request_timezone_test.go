package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const timezoneTestEnvironment = "<environment_context><current_date>2026-09-25</current_date><timezone>Asia/Tokyo</timezone><cwd>/workspace</cwd></environment_context>"

func timezoneTestBody() []byte {
	body, _ := json.Marshal(map[string]any{
		"type": "response.create", "model": "gpt-5.5",
		"input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": timezoneTestEnvironment}}}},
		"tools": []any{map[string]any{"type": "web_search", "user_location": map[string]any{"timezone": "Asia/Tokyo", "country": "JP"}}},
	})
	return body
}

func timezoneTestAccount(name string) *Account {
	return &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{openAIRequestTimezoneKey: name}}
}

func TestOpenAIRequestTimezoneValidationAndPreservation(t *testing.T) {
	for _, value := range []any{"America/Los_Angeles", "Asia/Seoul", "Asia/Tokyo", "UTC", "", nil} {
		require.NoError(t, ValidateOpenAIRequestTimezoneExtra(PlatformOpenAI, map[string]any{openAIRequestTimezoneKey: value}))
	}
	for _, value := range []any{true, 9, "Local", "America/Invalid", " Asia/Tokyo"} {
		require.Error(t, ValidateOpenAIRequestTimezoneExtra(PlatformOpenAI, map[string]any{openAIRequestTimezoneKey: value}))
	}
	require.Error(t, ValidateOpenAIRequestTimezoneExtra(PlatformAnthropic, map[string]any{openAIRequestTimezoneKey: "UTC"}))
	for _, value := range []any{"", nil} {
		next := map[string]any{openAIRequestTimezoneKey: value}
		preserveOpenAIRequestTimezone(timezoneTestAccount("Asia/Seoul").Extra, next)
		require.Equal(t, value, next[openAIRequestTimezoneKey], "explicit clear must win")
	}
	next := map[string]any{"other": true}
	preserveOpenAIRequestTimezone(timezoneTestAccount("Asia/Seoul").Extra, next)
	require.Equal(t, "Asia/Seoul", next[openAIRequestTimezoneKey])
	old := map[string]any{"other": true}
	preserveOpenAIRequestTimezone(nil, old)
	require.NotContains(t, old, openAIRequestTimezoneKey)
	_, err := buildAccountForCreate(&CreateAccountInput{Platform: PlatformOpenAI}, map[string]any{openAIRequestTimezoneKey: "Bad/Timezone"})
	require.Error(t, err)
	_, err = (&adminServiceImpl{}).BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{Extra: map[string]any{openAIRequestTimezoneKey: "UTC"}})
	require.Error(t, err, "bulk updates must not change account timezones")
}

func TestOpenAIRequestTimezoneOnlyExplicitAccount(t *testing.T) {
	body := timezoneTestBody()
	original := string(body)
	for _, account := range []*Account{nil, {Platform: PlatformOpenAI}, timezoneTestAccount(""), timezoneTestAccount("Invalid/Zone"), {Platform: PlatformAnthropic, Extra: timezoneTestAccount("UTC").Extra}} {
		require.Equal(t, original, string(applyOpenAIRequestTimezone(account, body)))
	}
	changed := applyOpenAIRequestTimezone(timezoneTestAccount("America/Los_Angeles"), body)
	require.Equal(t, strings.Replace(timezoneTestEnvironment, "Asia/Tokyo", "America/Los_Angeles", 1), gjson.GetBytes(changed, "input.0.content.0.text").String())
	require.Equal(t, "America/Los_Angeles", gjson.GetBytes(changed, "tools.0.user_location.timezone").String())
	require.Equal(t, "JP", gjson.GetBytes(changed, "tools.0.user_location.country").String())
	require.Equal(t, original, string(body), "canonical replay/failover input must not be mutated")
	require.Equal(t, original, string(applyOpenAIRequestTimezone(&Account{Platform: PlatformOpenAI}, body)))
	for _, text := range []string{
		"Example: " + timezoneTestEnvironment,
		"<environment_context><cwd>/workspace</cwd></environment_context>",
		"<environment_context><timezone>UTC</timezone><timezone>Asia/Tokyo</timezone></environment_context>",
		"<environment_context><timezone><nested>UTC</nested></timezone></environment_context>",
		"<environment_context><timezone>UTC</timezone>",
		timezoneTestEnvironment + " trailing text",
	} {
		require.Equal(t, text, rewriteOpenAIEnvironmentTimezone(text, "America/New_York"))
	}
	require.Equal(t, "  <environment_context><timezone>\n America/New_York \t</timezone></environment_context> ", rewriteOpenAIEnvironmentTimezone("  <environment_context><timezone>\n Asia/Tokyo \t</timezone></environment_context> ", "America/New_York"))
	assistant := []byte(strings.Replace(original, `"role":"user"`, `"role":"assistant"`, 1))
	require.Equal(t, timezoneTestEnvironment, gjson.GetBytes(applyOpenAIRequestTimezone(timezoneTestAccount("UTC"), assistant), "input.0.content.0.text").String())
}

func TestOpenAIRequestTimezoneHTTPOutboundIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	body := timezoneTestBody()
	original := string(body)
	for _, passthrough := range []bool{false, true} {
		for _, name := range []string{"America/Los_Angeles", "", "Asia/Seoul"} {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Request.Header.Set("Accept-Language", "zh-CN")
			var req *http.Request
			var err error
			if passthrough {
				req, err = svc.buildUpstreamRequestOpenAIPassthrough(context.Background(), c, timezoneTestAccount(name), body, "test-token")
			} else {
				req, err = svc.buildUpstreamRequest(context.Background(), c, timezoneTestAccount(name), body, "test-token", false, "", false)
			}
			require.NoError(t, err)
			wire, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.NoError(t, req.Body.Close())
			want := name
			if want == "" {
				want = "Asia/Tokyo"
			}
			require.Contains(t, gjson.GetBytes(wire, "input.0.content.0.text").String(), "<timezone>"+want+"</timezone>")
			require.Equal(t, original, string(body))
		}
	}
}

func TestOpenAIRequestTimezoneWSPayloadDoesNotMutateReplay(t *testing.T) {
	var payload map[string]any
	require.NoError(t, json.Unmarshal(timezoneTestBody(), &payload))
	for _, name := range []string{"America/New_York", "", "Asia/Seoul"} {
		wire, err := json.Marshal(openAIRequestTimezoneWSPayload(timezoneTestAccount(name), payload))
		require.NoError(t, err)
		want := name
		if want == "" {
			want = "Asia/Tokyo"
		}
		require.Contains(t, gjson.GetBytes(wire, "input.0.content.0.text").String(), "<timezone>"+want+"</timezone>")
	}
	unchanged, err := json.Marshal(payload)
	require.NoError(t, err)
	require.Equal(t, timezoneTestEnvironment, gjson.GetBytes(unchanged, "input.0.content.0.text").String())
}

func TestOpenAIRequestTimezonePassthroughWSTwoTurns(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	upstream := newStagedPassthroughConn()
	account := passthroughLifecycleAccount()
	account.Extra[openAIRequestTimezoneKey] = "America/New_York"
	svc := newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream)
	server, done := startPassthroughLifecycleServer(t, ctx, svc, account)
	t.Logf("temporary websocket listener: %s", server.Listener.Addr())
	defer server.Close()
	client := dialPassthroughLifecycleClientWithPayload(t, server, string(timezoneTestBody()))
	defer client.CloseNow()
	for turn := 0; turn < 2; turn++ {
		wire := requirePassthroughUpstreamWrite(t, upstream, 3*time.Second)
		require.Contains(t, gjson.GetBytes(wire, "input.0.content.0.text").String(), "<timezone>America/New_York</timezone>")
		upstream.Send(`{"type":"response.completed","response":{"id":"resp_tz","status":"completed","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`)
		_, err := readPassthroughLifecycleFrame(t, client, 3*time.Second)
		require.NoError(t, err)
		if turn == 0 {
			writeCtx, stop := context.WithTimeout(ctx, 3*time.Second)
			err := client.Write(writeCtx, coderws.MessageText, timezoneTestBody())
			stop()
			require.NoError(t, err)
		}
	}
	require.NoError(t, client.Close(coderws.StatusNormalClosure, "done"))
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("websocket session did not exit")
	}
}

func TestOpenAIRequestTimezoneHTTPToWSForward(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := passthroughLifecycleConfig()
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	conn := &openAIWSCaptureConn{events: [][]byte{[]byte(`{"type":"response.completed","response":{"id":"resp_timezone","status":"completed","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`)}}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(&openAIWSCaptureDialer{conn: conn})
	defer pool.Close()
	svc := &OpenAIGatewayService{
		cfg: cfg, httpUpstream: &httpUpstreamRecorder{}, cache: &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool,
	}
	account := &Account{ID: 43, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "test-token"},
		Extra:       map[string]any{"responses_websockets_v2_enabled": true, openAIRequestTimezoneKey: "America/Los_Angeles"},
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	body := timezoneTestBody()
	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.True(t, result.OpenAIWSMode)
	wire, err := json.Marshal(conn.lastWrite)
	require.NoError(t, err)
	require.Contains(t, gjson.GetBytes(wire, "input.0.content.0.text").String(), "<timezone>America/Los_Angeles</timezone>")
	require.Equal(t, timezoneTestEnvironment, gjson.GetBytes(body, "input.0.content.0.text").String())
}
