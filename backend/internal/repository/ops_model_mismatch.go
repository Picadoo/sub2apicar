package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Query only audited mismatches in the evaluation window, using the existing
// partial index. Aggregate before limiting so the alert threshold is exact.
// Date suffixes are ignored only for GPT aliases; unrelated model families,
// sizes and arbitrary suffixes must remain distinct. Raw audit rows stay intact.
const opsModelMismatchGroupsSQL = `
WITH candidates AS (
 SELECT u.account_id, u.model AS requested_model,
        COALESCE(NULLIF(BTRIM(u.upstream_model), ''), u.model) AS sent_model,
        BTRIM(u.upstream_response_model) AS response_model
 FROM usage_logs u
 JOIN accounts a ON a.id = u.account_id
 WHERE u.upstream_model_mismatch IS TRUE
   AND u.created_at >= $1 AND u.created_at < $2
   AND ($3 = '' OR a.platform = $3)
   AND ($4::bigint IS NULL OR u.group_id = $4)
   AND NULLIF(BTRIM(u.upstream_response_model), '') IS NOT NULL
), mismatches AS (
 SELECT * FROM candidates
 WHERE CASE WHEN LOWER(sent_model) LIKE 'gpt-%'
       THEN regexp_replace(LOWER(sent_model), '-[0-9]{4}-?[0-9]{2}-?[0-9]{2}$', '')
       ELSE LOWER(sent_model) END
    <> CASE WHEN LOWER(response_model) LIKE 'gpt-%'
       THEN regexp_replace(LOWER(response_model), '-[0-9]{4}-?[0-9]{2}-?[0-9]{2}$', '')
       ELSE LOWER(response_model) END
)
SELECT account_id, requested_model, sent_model, response_model,
       COUNT(*) AS occurrences, SUM(COUNT(*)) OVER ()::bigint AS total
FROM mismatches
GROUP BY account_id, requested_model, sent_model, response_model
ORDER BY occurrences DESC, account_id, requested_model, sent_model, response_model
LIMIT 10`

func (r *opsRepository) GetModelMismatchGroups(ctx context.Context, start, end time.Time, platform string, groupID *int64) ([]service.OpsModelMismatchGroup, error) {
	rows, err := r.db.QueryContext(ctx, opsModelMismatchGroupsSQL, start, end, platform, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := make([]service.OpsModelMismatchGroup, 0, 10)
	for rows.Next() {
		var group service.OpsModelMismatchGroup
		if err := rows.Scan(&group.AccountID, &group.RequestedModel, &group.SentModel, &group.ResponseModel, &group.Count, &group.Total); err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}
