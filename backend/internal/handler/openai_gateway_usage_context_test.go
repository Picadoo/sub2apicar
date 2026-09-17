package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type asyncUsageLogCapture struct {
	service.UsageLogRepository

	done chan struct{}
	once sync.Once
	mu   sync.Mutex
	log  *service.UsageLog
}

func (c *asyncUsageLogCapture) Create(_ context.Context, log *service.UsageLog) (bool, error) {
	c.mu.Lock()
	c.log = log
	c.mu.Unlock()
	c.once.Do(func() { close(c.done) })
	return true, nil
}

type switchingUsageRecordContext struct {
	context.Context

	mu                sync.RWMutex
	values            map[any]any
	replaced          atomic.Bool
	readsAfterReplace atomic.Int32
}

func (c *switchingUsageRecordContext) Value(key any) any {
	if c.replaced.Load() {
		c.readsAfterReplace.Add(1)
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.values[key]
}

func (c *switchingUsageRecordContext) replace(values map[any]any) {
	c.mu.Lock()
	c.values = values
	c.mu.Unlock()
	c.replaced.Store(true)
}

func TestSubmitUsageRecordTaskCopiesRequestContext(t *testing.T) {
	parent := context.WithValue(context.Background(), ctxkey.ClientRequestID, "client-request-123")
	parent = context.WithValue(parent, ctxkey.RequestID, "request-456")

	var gotClientRequestID string
	var gotRequestID string
	h := &GatewayHandler{}
	h.submitUsageRecordTask(parent, func(ctx context.Context) {
		gotClientRequestID, _ = ctx.Value(ctxkey.ClientRequestID).(string)
		gotRequestID, _ = ctx.Value(ctxkey.RequestID).(string)
	})

	require.Equal(t, "client-request-123", gotClientRequestID)
	require.Equal(t, "request-456", gotRequestID)
}

func TestOpenAISubmitUsageRecordTaskCopiesRequestContext(t *testing.T) {
	parent := context.WithValue(context.Background(), ctxkey.ClientRequestID, "openai-client-request-123")
	parent = context.WithValue(parent, ctxkey.RequestID, "openai-request-456")

	var gotClientRequestID string
	var gotRequestID string
	h := &OpenAIGatewayHandler{}
	h.submitUsageRecordTask(parent, func(ctx context.Context) {
		gotClientRequestID, _ = ctx.Value(ctxkey.ClientRequestID).(string)
		gotRequestID, _ = ctx.Value(ctxkey.RequestID).(string)
	})

	require.Equal(t, "openai-client-request-123", gotClientRequestID)
	require.Equal(t, "openai-request-456", gotRequestID)
}

func TestUsageRecordTaskSnapshotsParentIDsBeforeWorkerExecution(t *testing.T) {
	pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{
		WorkerCount:           1,
		QueueSize:             1,
		TaskTimeout:           time.Second,
		OverflowPolicy:        "drop",
		OverflowSamplePercent: 0,
		AutoScaleEnabled:      false,
	})
	t.Cleanup(pool.Stop)

	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	require.Equal(t, service.UsageRecordSubmitModeEnqueued, pool.Submit(func(ctx context.Context) {
		close(started)
		<-release
	}))
	<-started

	parent := &switchingUsageRecordContext{
		Context: context.Background(),
		values: map[any]any{
			ctxkey.ClientRequestID: "client-request-before-reuse",
			ctxkey.RequestID:       "request-before-reuse",
		},
	}
	var gotClientRequestID, gotRequestID string
	var gotDeadline bool
	var gotDone <-chan struct{}
	taskDone := make(chan struct{})
	h := &GatewayHandler{usageRecordWorkerPool: pool}
	h.submitUsageRecordTask(parent, func(ctx context.Context) {
		gotClientRequestID, _ = ctx.Value(ctxkey.ClientRequestID).(string)
		gotRequestID, _ = ctx.Value(ctxkey.RequestID).(string)
		_, gotDeadline = ctx.Deadline()
		gotDone = ctx.Done()
		close(taskDone)
	})

	parent.replace(map[any]any{
		ctxkey.ClientRequestID: "client-request-after-reuse",
		ctxkey.RequestID:       "request-after-reuse",
	})
	releaseOnce.Do(func() { close(release) })
	select {
	case <-taskDone:
	case <-time.After(time.Second):
		t.Fatal("usage task did not execute")
	}

	require.Equal(t, "client-request-before-reuse", gotClientRequestID)
	require.Equal(t, "request-before-reuse", gotRequestID)
	require.True(t, gotDeadline, "worker context deadline must be preserved")
	require.NotNil(t, gotDone, "worker context cancellation must be preserved")
	require.Zero(t, parent.readsAfterReplace.Load(), "parent context must not be read during task execution")
}

func TestGrokVoiceUsageSnapshotsRequestedModelBeforeWorkerExecution(t *testing.T) {
	gin.SetMode(gin.TestMode)
	usageLogs := &asyncUsageLogCapture{done: make(chan struct{})}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	gatewayService := service.NewOpenAIGatewayService(
		nil,
		usageLogs,
		nil,
		nil,
		nil,
		nil,
		nil,
		cfg,
		nil,
		nil,
		service.NewBillingService(cfg, nil),
		nil,
		nil,
		nil,
		service.NewDeferredService(nil, nil, 0),
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{
		WorkerCount:           1,
		QueueSize:             1,
		TaskTimeout:           time.Second,
		OverflowPolicy:        config.UsageRecordOverflowPolicyDrop,
		OverflowSamplePercent: 0,
		AutoScaleEnabled:      false,
	})
	t.Cleanup(pool.Stop)

	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	require.Equal(t, service.UsageRecordSubmitModeEnqueued, pool.Submit(func(ctx context.Context) {
		close(started)
		<-release
	}))
	<-started

	groupID := int64(701)
	apiKey := &service.APIKey{
		ID:      702,
		UserID:  703,
		GroupID: &groupID,
		User:    &service.User{ID: 703, Status: service.StatusActive},
		Group:   &service.Group{ID: groupID, Platform: service.PlatformGrok, Status: service.StatusActive},
	}
	account := &service.Account{ID: 704, Platform: service.PlatformGrok, Type: service.AccountTypeAPIKey}
	result := &service.OpenAIForwardResult{
		RequestID:     "voice-request-705",
		Model:         "grok-voice",
		UpstreamModel: "grok-voice",
		AudioUsage:    &service.AudioUsage{Mode: "tts", DurationOrUnits: 1},
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	originalContext := context.WithValue(context.Background(), ctxkey.RequestedPublicModel, "grok-original-model")
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/audio/speech", nil).WithContext(originalContext)
	h := &OpenAIGatewayHandler{
		gatewayService:        gatewayService,
		usageRecordWorkerPool: pool,
	}

	h.recordGrokVoiceUsage(c, apiKey, account, nil, "audio/speech", nil, result)
	c.Request = c.Request.WithContext(context.WithValue(context.Background(), ctxkey.RequestedPublicModel, "grok-reused-model"))
	releaseOnce.Do(func() { close(release) })

	select {
	case <-usageLogs.done:
	case <-time.After(time.Second):
		t.Fatal("usage record did not execute")
	}
	usageLogs.mu.Lock()
	got := usageLogs.log
	usageLogs.mu.Unlock()
	require.NotNil(t, got)
	require.Equal(t, "grok-original-model", got.RequestedModel)
}
