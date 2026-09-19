package service

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// OpsModelMismatchGroup is an admin-only aggregate; it contains no credentials
// or request bodies. Total counts every matching group, not just the sample.
type OpsModelMismatchGroup struct {
	AccountID      int64
	RequestedModel string
	SentModel      string
	ResponseModel  string
	Count          int64
	Total          int64
}

type opsModelMismatchReader interface {
	GetModelMismatchGroups(context.Context, time.Time, time.Time, string, *int64) ([]OpsModelMismatchGroup, error)
}

func (s *OpsAlertEvaluatorService) computeRuleMetricWithDetails(ctx context.Context, rule *OpsAlertRule, metrics *OpsSystemMetricsSnapshot, start, end time.Time, platform string, groupID *int64) (float64, string, bool) {
	if rule == nil || strings.TrimSpace(rule.MetricType) != "upstream_model_mismatch_count" {
		value, ok := s.computeRuleMetric(ctx, rule, metrics, start, end, platform, groupID)
		return value, "", ok
	}
	reader, ok := s.opsRepo.(opsModelMismatchReader)
	if !ok {
		return 0, "", false
	}
	groups, err := reader.GetModelMismatchGroups(ctx, start, end, platform, groupID)
	if err != nil {
		// Unknown is not zero: query failures must not resolve an active alert.
		return 0, "", false
	}
	if len(groups) == 0 {
		return 0, "", true
	}
	var details strings.Builder
	details.WriteString("\n上游响应自报模型不一致（非模型质量鉴定；未自动重试或切换账号）。最多列出 10 组：")
	for i, group := range groups {
		if i >= 10 {
			break
		}
		fmt.Fprintf(&details, "\n账号 #%d：请求 %s；发往上游 %s → 响应 %s；%d 次", group.AccountID, group.RequestedModel, group.SentModel, group.ResponseModel, group.Count)
	}
	return float64(groups[0].Total), details.String(), true
}
