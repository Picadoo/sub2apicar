package service

import (
	"context"
	"sync"
	"time"
)

// AccountWindowQuotaResetService 周期性扫描已到期的官方窗口并清零用户分摊台账。
// 这是兜底机制：正常情况下官方利用率回落会在 Attribute 中被 Lua 基线脚本即时识别并触发重置；
// 但当某账号在窗口刷新时刻恰好无流量（无法观测到回落）时，靠本服务按 window_reset_at 定时清零。
type AccountWindowQuotaResetService struct {
	quota    *AccountWindowQuotaService
	interval time.Duration
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// NewAccountWindowQuotaResetService 构造定时重置服务。
func NewAccountWindowQuotaResetService(quota *AccountWindowQuotaService, interval time.Duration) *AccountWindowQuotaResetService {
	return &AccountWindowQuotaResetService{
		quota:    quota,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

// Start 启动后台定时循环（依赖缺失或间隔非法时直接返回，不启动 goroutine）。
func (s *AccountWindowQuotaResetService) Start() {
	if s == nil || s.quota == nil || !s.quota.Enabled() || s.interval <= 0 {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()

		s.runOnce()
		for {
			select {
			case <-ticker.C:
				s.runOnce()
			case <-s.stopCh:
				return
			}
		}
	}()
}

// Stop 停止后台循环并等待退出。
func (s *AccountWindowQuotaResetService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	s.wg.Wait()
}

func (s *AccountWindowQuotaResetService) runOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.quota.ResetDueWindows(ctx)
}
