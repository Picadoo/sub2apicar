package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"

	"github.com/gin-gonic/gin"
)

// defaultChatProxyStatusURL 是网页端共享代理"看门人"(chatgate)的本机状态接口。
// 可用环境变量 CHAT_PROXY_STATUS_URL 覆盖。
const defaultChatProxyStatusURL = "http://127.0.0.1:5010/status"

// ChatProxyStatus 透传 chatgate 的在线状态(当前在用的代理账号 + 名额),
// 供仪表盘给全员展示"网页版 ChatGPT 现在谁在用、满没满"。
// 无依赖、fail-soft:看门人不可达 / 未部署时返回 enabled:false(前端卡片自动隐藏)。
// GET /api/v1/user/chat-proxy-status
func ChatProxyStatus(c *gin.Context) {
	url := os.Getenv("CHAT_PROXY_STATUS_URL")
	if url == "" {
		url = defaultChatProxyStatusURL
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	unavailable := func() {
		response.Success(c, gin.H{"enabled": false, "max": 0, "active": []any{}})
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		unavailable()
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		unavailable()
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		unavailable()
		return
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		unavailable()
		return
	}
	response.Success(c, payload)
}
