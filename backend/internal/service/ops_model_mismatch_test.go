package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type modelMismatchOpsRepo struct {
	OpsRepository
	rule     *OpsAlertRule
	active   *OpsAlertEvent
	latest   *OpsAlertEvent
	created  int
	groups   []OpsModelMismatchGroup
	err      error
	platform string
	groupID  *int64
}

func (r *modelMismatchOpsRepo) ListAlertRules(context.Context) ([]*OpsAlertRule, error) {
	return []*OpsAlertRule{r.rule}, nil
}
func (r *modelMismatchOpsRepo) GetLatestSystemMetrics(context.Context, int) (*OpsSystemMetricsSnapshot, error) {
	return nil, nil
}
func (r *modelMismatchOpsRepo) GetActiveAlertEvent(context.Context, int64) (*OpsAlertEvent, error) {
	return r.active, nil
}
func (r *modelMismatchOpsRepo) GetLatestAlertEvent(context.Context, int64) (*OpsAlertEvent, error) {
	return r.latest, nil
}
func (r *modelMismatchOpsRepo) CreateAlertEvent(_ context.Context, event *OpsAlertEvent) (*OpsAlertEvent, error) {
	r.created++
	event.ID = int64(r.created)
	r.active, r.latest = event, event
	return event, nil
}
func (r *modelMismatchOpsRepo) UpdateAlertEventStatus(_ context.Context, _ int64, status string, _ *time.Time) error {
	r.active.Status = status
	r.active = nil
	return nil
}
func (r *modelMismatchOpsRepo) UpsertJobHeartbeat(context.Context, *OpsUpsertJobHeartbeatInput) error {
	return nil
}

func TestOpsModelMismatchAlertLifecycle(t *testing.T) {
	repo := &modelMismatchOpsRepo{
		rule:   &OpsAlertRule{ID: 1, Name: "模型不一致", MetricType: "upstream_model_mismatch_count", Enabled: true, Operator: ">=", Threshold: 1, WindowMinutes: 5, CooldownMinutes: 10},
		groups: []OpsModelMismatchGroup{{AccountID: 51, SentModel: "gpt-6-astra", ResponseModel: "gpt-5.6-luna", Count: 3, Total: 3}},
	}
	s := NewOpsAlertEvaluatorService(nil, repo, nil, nil, nil, nil)
	s.evaluateOnce(time.Minute)
	require.Equal(t, 1, repo.created)
	require.Contains(t, repo.active.Description, "账号 #51")
	s.evaluateOnce(time.Minute)
	require.Equal(t, 1, repo.created, "active events deduplicate repeated mismatches")
	repo.err = errors.New("query failed")
	s.evaluateOnce(time.Minute)
	require.NotNil(t, repo.active, "failed query must not clear the alert")
	repo.err = nil
	repo.groups = nil
	s.evaluateOnce(time.Minute)
	require.Nil(t, repo.active)
	require.Equal(t, OpsAlertStatusResolved, repo.latest.Status)
	repo.groups = []OpsModelMismatchGroup{{Total: 1}}
	s.evaluateOnce(time.Minute)
	require.Equal(t, 1, repo.created, "cooldown prevents a new alert immediately after recovery")
	repo.latest.FiredAt = time.Now().Add(-11 * time.Minute)
	repo.rule.Enabled = false
	s.evaluateOnce(time.Minute)
	require.Equal(t, 1, repo.created, "disabled rules do not alert")
	repo.rule.Enabled = true
	s.evaluateOnce(time.Minute)
	require.Equal(t, 2, repo.created)
}

func (r *modelMismatchOpsRepo) GetModelMismatchGroups(_ context.Context, start, end time.Time, platform string, groupID *int64) ([]OpsModelMismatchGroup, error) {
	r.platform, r.groupID = platform, groupID
	return r.groups, r.err
}

func TestOpsModelMismatchMetric(t *testing.T) {
	groupID := int64(8)
	repo := &modelMismatchOpsRepo{groups: []OpsModelMismatchGroup{{AccountID: 51, RequestedModel: "client-alias", SentModel: "gpt-6-astra", ResponseModel: "gpt-5.6-luna", Count: 7, Total: 23}}}
	s := &OpsAlertEvaluatorService{opsRepo: repo}
	rule := &OpsAlertRule{MetricType: "upstream_model_mismatch_count"}
	end := time.Now()
	value, details, ok := s.computeRuleMetricWithDetails(context.Background(), rule, nil, end.Add(-5*time.Minute), end, "openai", &groupID)
	require.True(t, ok)
	require.Equal(t, float64(23), value, "total must not be limited to displayed groups")
	require.Contains(t, details, "账号 #51")
	require.Contains(t, details, "请求 client-alias")
	require.Contains(t, details, "gpt-6-astra → 响应 gpt-5.6-luna；7 次")
	require.Equal(t, "openai", repo.platform)
	require.Equal(t, &groupID, repo.groupID)
	require.True(t, compareMetric(value, ">=", 1))

	repo.groups = nil
	value, _, ok = s.computeRuleMetricWithDetails(context.Background(), rule, nil, end, end, "", nil)
	require.True(t, ok)
	require.Zero(t, value)

	repo.err = errors.New("database unavailable")
	_, _, ok = s.computeRuleMetricWithDetails(context.Background(), rule, nil, end, end, "", nil)
	require.False(t, ok, "unavailable must not resolve an active event as zero")

	s.opsRepo = nil
	_, _, ok = s.computeRuleMetricWithDetails(context.Background(), rule, nil, end, end, "", nil)
	require.False(t, ok)
}

func TestOpsModelMismatchMetricPreservesExistingMetrics(t *testing.T) {
	value := 42.0
	s := &OpsAlertEvaluatorService{}
	actual, details, ok := s.computeRuleMetricWithDetails(context.Background(), &OpsAlertRule{MetricType: "cpu_usage_percent"}, &OpsSystemMetricsSnapshot{CPUUsagePercent: &value}, time.Now(), time.Now(), "", nil)
	require.True(t, ok)
	require.Equal(t, value, actual)
	require.Empty(t, details)
}
