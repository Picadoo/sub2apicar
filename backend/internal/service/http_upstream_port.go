package service

import (
	"context"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

type httpUpstreamResponseHeadersObservedAtKey struct{}

// MarkHTTPUpstreamResponseHeadersObservedAt records when response headers were
// returned by the transport. The marker lives in the response request context,
// so it is never forwarded to clients as an HTTP header.
func MarkHTTPUpstreamResponseHeadersObservedAt(resp *http.Response, observedAt time.Time) {
	if resp == nil || resp.Request == nil || observedAt.IsZero() {
		return
	}
	ctx := context.WithValue(resp.Request.Context(), httpUpstreamResponseHeadersObservedAtKey{}, observedAt)
	resp.Request = resp.Request.WithContext(ctx)
}

// HTTPUpstreamResponseHeadersObservedAt returns the transport-level header
// observation time previously attached by the HTTP upstream implementation.
func HTTPUpstreamResponseHeadersObservedAt(resp *http.Response) time.Time {
	if resp == nil || resp.Request == nil {
		return time.Time{}
	}
	observedAt, _ := resp.Request.Context().Value(httpUpstreamResponseHeadersObservedAtKey{}).(time.Time)
	return observedAt
}

// HTTPUpstream 上游 HTTP 请求接口
// 用于向上游 API（Claude、OpenAI、Gemini 等）发送请求
type HTTPUpstream interface {
	// Do 执行 HTTP 请求（不启用 TLS 指纹）
	Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error)

	// DoWithTLS 执行带 TLS 指纹伪装的 HTTP 请求
	//
	// profile 参数:
	//   - nil: 不启用 TLS 指纹，行为与 Do 方法相同
	//   - non-nil: 使用指定的 Profile 进行 TLS 指纹伪装
	//
	// Profile 由调用方通过 TLSFingerprintProfileService 解析后传入，
	// 支持按账号绑定的数据库 profile 或内置默认 profile。
	DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error)
}
