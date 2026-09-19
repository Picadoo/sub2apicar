package admin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpsModelMismatchRuleValidation(t *testing.T) {
	var payload map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(`{"name":"模型不一致提醒","metric_type":"upstream_model_mismatch_count","operator":">=","threshold":1,"window_minutes":5,"cooldown_minutes":10,"notify_email":false}`), &payload))
	rule, err := validateOpsAlertRulePayload(payload)
	require.NoError(t, err)
	require.Equal(t, "upstream_model_mismatch_count", rule.MetricType)
	require.False(t, rule.NotifyEmail)
	payload["threshold"] = json.RawMessage(`200`)
	_, err = validateOpsAlertRulePayload(payload)
	require.NoError(t, err, "count is not a percentage")
	payload["threshold"] = json.RawMessage(`-1`)
	_, err = validateOpsAlertRulePayload(payload)
	require.Error(t, err)
}
