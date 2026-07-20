package repository

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// userAccountWindowQuotaRepository 是 service.UserAccountWindowQuotaRepository 的 ent 实现。
// 记录 用户 × 上游账号 × 官方窗口（5h/7d）维度的"按官方利用率百分比"配额台账。
type userAccountWindowQuotaRepository struct {
	client *dbent.Client
}

// NewUserAccountWindowQuotaRepository 创建 service.UserAccountWindowQuotaRepository 实现（DI Strategy A：直接返回接口）。
func NewUserAccountWindowQuotaRepository(client *dbent.Client) service.UserAccountWindowQuotaRepository {
	return &userAccountWindowQuotaRepository{client: client}
}

// RecomputeWindowSharesFromCheckpoint 使用成员均分检查点和检查点后的实际 Token 占比确定性重算。
// 同一组 usage_logs 无论由谁碰巧读到官方快照，结果都完全一致。
func (r *userAccountWindowQuotaRepository) RecomputeWindowSharesFromCheckpoint(ctx context.Context, accountID int64, window string, officialPercent float64, windowStart, checkpointAt time.Time, baseShares map[int64]float64, resetAt *time.Time) error {
	if officialPercent < 0 {
		officialPercent = 0
	}
	return r.withWindowQuotaTx(ctx, func(txCtx context.Context, client *dbent.Client) error {
		const rowsQ = `SELECT q.id, q.user_id
			FROM user_account_window_quotas q
			JOIN accounts a ON a.id = q.account_id
			WHERE q.account_id = $1 AND q.window_type = $2
			  AND q.deleted_at IS NULL AND a.deleted_at IS NULL
			  AND COALESCE(a.extra->>'window_quota_shared', 'false') = 'true'
			ORDER BY q.user_id
			FOR UPDATE`
		rows, err := client.QueryContext(txCtx, rowsQ, accountID, window)
		if err != nil {
			return fmt.Errorf("lock account %d window %s before token recompute: %w", accountID, window, err)
		}
		type memberUsage struct {
			id     int64
			userID int64
			base   float64
			tokens int64
		}
		var members []memberUsage
		for rows.Next() {
			var member memberUsage
			if err := rows.Scan(&member.id, &member.userID); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan account %d window %s member: %w", accountID, window, err)
			}
			members = append(members, member)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("iterate account %d window %s members: %w", accountID, window, err)
		}
		_ = rows.Close()
		if len(members) == 0 {
			return nil
		}

		tokenStart := checkpointAt
		useBase := checkpointAt.After(windowStart)
		if !useBase {
			tokenStart = windowStart
		}
		baseTotal := 0.0
		for i := range members {
			if useBase {
				members[i].base = math.Max(0, baseShares[members[i].userID])
				baseTotal += members[i].base
			}
		}

		placeholders := make([]string, len(members))
		tokenArgs := make([]any, 0, len(members)+2)
		tokenArgs = append(tokenArgs, accountID, tokenStart)
		for i, member := range members {
			placeholders[i] = fmt.Sprintf("$%d", i+3)
			tokenArgs = append(tokenArgs, member.userID)
		}
		tokensQ := `SELECT user_id,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0)::bigint
			FROM usage_logs
			WHERE account_id = $1 AND created_at >= $2 AND user_id IN (` + strings.Join(placeholders, ",") + `)
			GROUP BY user_id`
		tokenRows, err := client.QueryContext(txCtx, tokensQ, tokenArgs...)
		if err != nil {
			return fmt.Errorf("read account %d window %s checkpoint tokens: %w", accountID, window, err)
		}
		tokensByUser := make(map[int64]int64, len(members))
		totalTokens := int64(0)
		for tokenRows.Next() {
			var userID, tokens int64
			if err := tokenRows.Scan(&userID, &tokens); err != nil {
				_ = tokenRows.Close()
				return fmt.Errorf("scan account %d window %s checkpoint tokens: %w", accountID, window, err)
			}
			if tokens > 0 {
				tokensByUser[userID] = tokens
				totalTokens += tokens
			}
		}
		if err := tokenRows.Err(); err != nil {
			_ = tokenRows.Close()
			return fmt.Errorf("iterate account %d window %s checkpoint tokens: %w", accountID, window, err)
		}
		_ = tokenRows.Close()
		for i := range members {
			members[i].tokens = tokensByUser[members[i].userID]
		}

		target := math.Round(officialPercent*10000) / 10000
		rawValues := make([]float64, len(members))
		switch {
		case baseTotal > 0 && target <= baseTotal:
			for i, member := range members {
				rawValues[i] = target * member.base / baseTotal
			}
		case target > baseTotal && totalTokens > 0:
			extra := target - baseTotal
			for i, member := range members {
				rawValues[i] = member.base + extra*float64(member.tokens)/float64(totalTokens)
			}
		case baseTotal > 0:
			extra := target - baseTotal
			for i, member := range members {
				rawValues[i] = member.base + extra*member.base/baseTotal
			}
		default:
			for i := range members {
				rawValues[i] = target / float64(len(members))
			}
		}

		remaining := target
		now := time.Now()
		const updateQ = `UPDATE user_account_window_quotas
			SET attributed_percent = $2,
				window_reset_at = COALESCE($3, window_reset_at),
				updated_at = $4
			WHERE id = $1 AND deleted_at IS NULL`
		for i, member := range members {
			value := remaining
			if i < len(members)-1 {
				value = math.Floor(math.Max(0, rawValues[i])*10000) / 10000
				remaining -= value
			} else {
				value = math.Round(remaining*10000) / 10000
			}
			if value < 0 {
				value = 0
			}
			if _, err := client.ExecContext(txCtx, updateQ, member.id, value, nullableTime(resetAt), now); err != nil {
				return fmt.Errorf("write account %d window %s token share for user %d: %w", accountID, window, member.userID, err)
			}
		}
		return nil
	})
}

// ResetWindowForAccount 把某账号某窗口下所有活跃记录的 attributed_percent 清零并刷新 window_reset_at。
// 仅 5h 窗口重置时清零 donate_pool_fraction（5h 份额短、每窗口重新决定）；
// 7d 捐赠是周级长期承诺，永久保留、绝不自动清（含跨周重置），只在用户手动拖滑块时改变。
func (r *userAccountWindowQuotaRepository) ResetWindowForAccount(ctx context.Context, accountID int64, window string, newResetAt *time.Time) error {
	client := clientFromContext(ctx, r.client)
	const q = `UPDATE user_account_window_quotas
		SET attributed_percent = 0,
			donate_pool_fraction = CASE WHEN $2 = '5h' THEN 0 ELSE donate_pool_fraction END,
			window_reset_at = $3, updated_at = $4
		WHERE account_id = $1 AND window_type = $2 AND deleted_at IS NULL`
	_, err := client.ExecContext(ctx, q, accountID, window, nullableTime(newResetAt), time.Now())
	return err
}

// GetByUserAccountWindow 查询拼车账号的单条活跃配额。私人、已删除账号均视为未配置。
func (r *userAccountWindowQuotaRepository) GetByUserAccountWindow(ctx context.Context, userID, accountID int64, window string) (*service.UserAccountWindowQuotaRecord, error) {
	client := clientFromContext(ctx, r.client)
	const q = `SELECT q.user_id, q.account_id, q.window_type, q.limit_percent, q.attributed_percent,
			q.window_reset_at, COALESCE(q.donate_pool_fraction, 0)
		FROM user_account_window_quotas q
		JOIN accounts a ON a.id = q.account_id
		WHERE q.user_id = $1 AND q.account_id = $2 AND q.window_type = $3
		  AND q.deleted_at IS NULL AND a.deleted_at IS NULL
		  AND COALESCE(a.extra->>'window_quota_shared', 'false') = 'true'`

	rows, err := client.QueryContext(ctx, q, userID, accountID, window)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, nil
	}

	var rec service.UserAccountWindowQuotaRecord
	var resetAt sql.NullTime
	if err := rows.Scan(
		&rec.UserID,
		&rec.AccountID,
		&rec.WindowType,
		&rec.LimitPercent,
		&rec.AttributedPercent,
		&resetAt,
		&rec.DonatePoolFraction,
	); err != nil {
		return nil, err
	}
	if resetAt.Valid {
		t := resetAt.Time
		rec.WindowResetAt = &t
	}
	return &rec, nil
}

// ListByUser 返回用户在所有账号所有窗口的活跃配额（裸 SQL 以带出 donate_pool_fraction）。
func (r *userAccountWindowQuotaRepository) ListByUser(ctx context.Context, userID int64) ([]service.UserAccountWindowQuotaRecord, error) {
	client := clientFromContext(ctx, r.client)
	const q = `SELECT q.user_id, q.account_id, q.window_type, q.limit_percent, q.attributed_percent, q.window_reset_at, COALESCE(q.donate_pool_fraction, 0)
		FROM user_account_window_quotas q
		JOIN accounts a ON a.id = q.account_id
		WHERE q.user_id = $1 AND q.deleted_at IS NULL AND a.deleted_at IS NULL
		  AND COALESCE(a.extra->>'window_quota_shared', 'false') = 'true'`
	rows, err := client.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanWindowQuotaRecords(rows)
}

// ListByAccount 返回某账号下所有用户所有窗口的活跃配额（裸 SQL 以带出 donate_pool_fraction）。
func (r *userAccountWindowQuotaRepository) ListByAccount(ctx context.Context, accountID int64) ([]service.UserAccountWindowQuotaRecord, error) {
	const q = `SELECT user_id, account_id, window_type, limit_percent, attributed_percent, window_reset_at,
		COALESCE(donate_pool_fraction, 0)
		FROM user_account_window_quotas
		WHERE account_id = $1 AND deleted_at IS NULL
		ORDER BY user_id, window_type`
	client := r.client
	rows, err := client.QueryContext(ctx, q, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanWindowQuotaRecords(rows)
}

// ListSharedAccountIDs 返回所有启用拼车额度的 OpenAI 账号，包括尚未创建成员额度行的账号。
func (r *userAccountWindowQuotaRepository) ListSharedAccountIDs(ctx context.Context) ([]int64, error) {
	const q = `SELECT id
		FROM accounts
		WHERE deleted_at IS NULL
		  AND platform = 'openai'
		  AND COALESCE(extra->>'window_quota_shared', 'false') = 'true'
		ORDER BY id`
	rows, err := r.client.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accountIDs := make([]int64, 0)
	for rows.Next() {
		var accountID int64
		if err := rows.Scan(&accountID); err != nil {
			return nil, err
		}
		accountIDs = append(accountIDs, accountID)
	}
	return accountIDs, rows.Err()
}

// ListPeerSharedAccountMemberUserIDs 返回与目标账号同分组的既有拼车账号成员。
// 新拼车号用这批成员初始化额度行，避免“已勾选拼车但 0 成员、仪表盘不可见”。
func (r *userAccountWindowQuotaRepository) ListPeerSharedAccountMemberUserIDs(ctx context.Context, accountID int64) ([]int64, error) {
	const q = `SELECT DISTINCT q.user_id
		FROM account_groups target_group
		JOIN account_groups peer_group
		  ON peer_group.group_id = target_group.group_id
		 AND peer_group.account_id <> target_group.account_id
		JOIN accounts peer_account
		  ON peer_account.id = peer_group.account_id
		 AND peer_account.deleted_at IS NULL
		 AND peer_account.platform = 'openai'
		 AND COALESCE(peer_account.extra->>'window_quota_shared', 'false') = 'true'
		JOIN user_account_window_quotas q
		  ON q.account_id = peer_account.id
		 AND q.deleted_at IS NULL
		JOIN users u
		  ON u.id = q.user_id
		 AND u.deleted_at IS NULL
		 AND u.status = 'active'
		WHERE target_group.account_id = $1
		ORDER BY q.user_id`
	rows, err := r.client.QueryContext(ctx, q, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	userIDs := make([]int64, 0)
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			return nil, err
		}
		userIDs = append(userIDs, userID)
	}
	return userIDs, rows.Err()
}

// scanWindowQuotaRecords 把配额查询结果集扫描为传输结构体（含 donate_pool_fraction）。
func scanWindowQuotaRecords(rows *sql.Rows) ([]service.UserAccountWindowQuotaRecord, error) {
	var out []service.UserAccountWindowQuotaRecord
	for rows.Next() {
		var rec service.UserAccountWindowQuotaRecord
		var resetAt sql.NullTime
		if err := rows.Scan(&rec.UserID, &rec.AccountID, &rec.WindowType, &rec.LimitPercent, &rec.AttributedPercent, &resetAt, &rec.DonatePoolFraction); err != nil {
			return nil, err
		}
		if resetAt.Valid {
			t := resetAt.Time
			rec.WindowResetAt = &t
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// SetDonatePoolFraction 在事务中锁定账号窗口全体成员，防止并发撤回已经被借用的捐赠额度。
func (r *userAccountWindowQuotaRepository) SetDonatePoolFraction(ctx context.Context, userID, accountID int64, window string, fraction, defaultLimit float64) error {
	return r.withWindowQuotaTx(ctx, func(txCtx context.Context, client *dbent.Client) error {
		const lockQ = `SELECT q.user_id, q.account_id, q.window_type, q.limit_percent, q.attributed_percent, q.window_reset_at, COALESCE(q.donate_pool_fraction, 0)
			FROM user_account_window_quotas q
			JOIN accounts a ON a.id = q.account_id
			WHERE q.account_id = $1 AND q.window_type = $2
			  AND q.deleted_at IS NULL AND a.deleted_at IS NULL
			  AND COALESCE(a.extra->>'window_quota_shared', 'false') = 'true'
			ORDER BY q.user_id
			FOR UPDATE`
		rows, err := client.QueryContext(txCtx, lockQ, accountID, window)
		if err != nil {
			return fmt.Errorf("lock account %d window %s before donation update: %w", accountID, window, err)
		}
		records, err := scanWindowQuotaRecords(rows)
		_ = rows.Close()
		if err != nil {
			return fmt.Errorf("scan account %d window %s before donation update: %w", accountID, window, err)
		}
		if err := service.ValidateAccountWindowDonateFraction(records, userID, window, fraction); err != nil {
			return err
		}

		const upsertQ = `INSERT INTO user_account_window_quotas
			(user_id, account_id, window_type, limit_percent, attributed_percent, donate_pool_fraction, created_at, updated_at)
			VALUES ($1, $2, $3, $4, 0, $5, $6, $6)
			ON CONFLICT (user_id, account_id, window_type) WHERE deleted_at IS NULL DO UPDATE SET
				donate_pool_fraction = EXCLUDED.donate_pool_fraction,
				updated_at           = EXCLUDED.updated_at`
		_, err = client.ExecContext(txCtx, upsertQ, userID, accountID, window, defaultLimit, fraction, time.Now())
		return err
	})
}

// ListDueResets 返回 window_reset_at 已到期且仍有用量的 (账号, 窗口) 去重列表。
func (r *userAccountWindowQuotaRepository) ListDueResets(ctx context.Context, now time.Time) ([]service.AccountWindowReset, error) {
	client := clientFromContext(ctx, r.client)
	const q = `SELECT DISTINCT q.account_id, q.window_type
		FROM user_account_window_quotas q
		JOIN accounts a ON a.id = q.account_id
		WHERE q.deleted_at IS NULL AND a.deleted_at IS NULL
		  AND COALESCE(a.extra->>'window_quota_shared', 'false') = 'true'
		  AND q.attributed_percent > 0
		  AND q.window_reset_at IS NOT NULL AND q.window_reset_at <= $1`
	rows, err := client.QueryContext(ctx, q, now)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []service.AccountWindowReset
	for rows.Next() {
		var rec service.AccountWindowReset
		if err := rows.Scan(&rec.AccountID, &rec.WindowType); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// SetLimitForUserAccount 设置（或新建）某 (user, account, window) 的 limit_percent。
func (r *userAccountWindowQuotaRepository) SetLimitForUserAccount(ctx context.Context, userID, accountID int64, window string, limitPercent float64) error {
	if window == "" {
		return fmt.Errorf("window_type is required")
	}
	client := clientFromContext(ctx, r.client)
	const q = `INSERT INTO user_account_window_quotas
		(user_id, account_id, window_type, limit_percent, attributed_percent, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 0, $5, $5)
		ON CONFLICT (user_id, account_id, window_type) WHERE deleted_at IS NULL DO UPDATE SET
			limit_percent = EXCLUDED.limit_percent,
			updated_at    = EXCLUDED.updated_at`
	_, err := client.ExecContext(ctx, q, userID, accountID, window, limitPercent, time.Now())
	return err
}

// ListAllWithUser 返回所有活跃配额行（JOIN users 带出邮箱/用户名），供管理端总览。
func (r *userAccountWindowQuotaRepository) ListAllWithUser(ctx context.Context) ([]service.AdminWindowQuotaOverviewRow, error) {
	client := clientFromContext(ctx, r.client)
	const q = `SELECT q.user_id, COALESCE(u.email, ''), COALESCE(u.username, ''),
			q.account_id, q.window_type, q.limit_percent, q.attributed_percent, q.window_reset_at,
			COALESCE(q.donate_pool_fraction, 0)
		FROM user_account_window_quotas q
		JOIN users u ON u.id = q.user_id
		JOIN accounts a ON a.id = q.account_id
		WHERE q.deleted_at IS NULL AND u.deleted_at IS NULL AND a.deleted_at IS NULL
		  AND COALESCE(a.extra->>'window_quota_shared', 'false') = 'true'
		ORDER BY q.account_id, q.user_id, q.window_type`
	rows, err := client.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []service.AdminWindowQuotaOverviewRow
	for rows.Next() {
		var rec service.AdminWindowQuotaOverviewRow
		var resetAt sql.NullTime
		if err := rows.Scan(&rec.UserID, &rec.Email, &rec.Username, &rec.AccountID, &rec.WindowType, &rec.LimitPercent, &rec.AttributedPercent, &resetAt, &rec.DonatePoolFraction); err != nil {
			return nil, err
		}
		if resetAt.Valid {
			t := resetAt.Time
			rec.WindowResetAt = &t
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// GetMaxConfiguredSumForWindow 返回指定窗口中各账号 configured sum 的最大值。
func (r *userAccountWindowQuotaRepository) GetMaxConfiguredSumForWindow(ctx context.Context, window string) (float64, error) {
	client := clientFromContext(ctx, r.client)
	const q = `SELECT COALESCE(MAX(configured_sum), 0)
		FROM (
			SELECT q.account_id, SUM(q.limit_percent) AS configured_sum
			FROM user_account_window_quotas q
			JOIN accounts a ON a.id = q.account_id
			WHERE q.window_type = $1 AND q.deleted_at IS NULL AND a.deleted_at IS NULL
			  AND COALESCE(a.extra->>'window_quota_shared', 'false') = 'true'
			GROUP BY q.account_id
		) configured_by_account`
	rows, err := client.QueryContext(ctx, q, window)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, err
		}
		return 0, nil
	}
	var maxConfigured float64
	if err := rows.Scan(&maxConfigured); err != nil {
		return 0, err
	}
	return maxConfigured, rows.Err()
}

// EqualizeActiveMemberLimits 在一个事务中把指定账号 5h/7d 的 limit 分别均分给各窗口活跃成员。
func (r *userAccountWindowQuotaRepository) EqualizeActiveMemberLimits(ctx context.Context, accountID int64, ceiling5h, ceiling7d float64) ([]service.AccountWindowEqualizationResult, error) {
	var results []service.AccountWindowEqualizationResult
	err := r.withWindowQuotaTx(ctx, func(txCtx context.Context, client *dbent.Client) error {
		for _, item := range []struct {
			window  string
			ceiling float64
		}{
			{window: service.WindowType5h, ceiling: ceiling5h},
			{window: service.WindowType7d, ceiling: ceiling7d},
		} {
			const membersQ = `SELECT q.user_id
				FROM user_account_window_quotas q
				JOIN accounts a ON a.id = q.account_id
				WHERE q.account_id = $1 AND q.window_type = $2 AND q.deleted_at IS NULL AND a.deleted_at IS NULL
				  AND COALESCE(a.extra->>'window_quota_shared', 'false') = 'true'
				ORDER BY q.user_id
				FOR UPDATE`
			rows, err := client.QueryContext(txCtx, membersQ, accountID, item.window)
			if err != nil {
				return fmt.Errorf("list active members for account %d window %s: %w", accountID, item.window, err)
			}
			memberCount := 0
			for rows.Next() {
				var userID int64
				if err := rows.Scan(&userID); err != nil {
					_ = rows.Close()
					return fmt.Errorf("scan active member for account %d window %s: %w", accountID, item.window, err)
				}
				memberCount++
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return fmt.Errorf("iterate active members for account %d window %s: %w", accountID, item.window, err)
			}
			_ = rows.Close()
			if memberCount == 0 {
				return fmt.Errorf("%w: account=%d window=%s", service.ErrAccountWindowNoActiveMembers, accountID, item.window)
			}

			// limit_percent 是 DECIMAL(7,4)：向下截断到可持久化精度，避免四舍五入后 memberCount×share 反而略超 ceiling。
			share := math.Floor(item.ceiling/float64(memberCount)*10000) / 10000
			const updateQ = `UPDATE user_account_window_quotas
				SET limit_percent = $3, updated_at = $4
				WHERE account_id = $1 AND window_type = $2 AND deleted_at IS NULL`
			res, err := client.ExecContext(txCtx, updateQ, accountID, item.window, share, time.Now())
			if err != nil {
				return fmt.Errorf("equalize account %d window %s limits: %w", accountID, item.window, err)
			}
			affected, err := res.RowsAffected()
			if err != nil {
				return fmt.Errorf("read equalize result for account %d window %s: %w", accountID, item.window, err)
			}
			if affected != int64(memberCount) {
				return fmt.Errorf("equalize account %d window %s affected %d rows, expected %d", accountID, item.window, affected, memberCount)
			}
			results = append(results, service.AccountWindowEqualizationResult{
				WindowType:        item.window,
				ActiveMemberCount: memberCount,
				SharePercent:      share,
				CeilingPercent:    item.ceiling,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// SyncAccountMembers 以管理员显式选择为准同步账号成员，并在同一事务中为 5h/7d 创建或更新均分额度。
func (r *userAccountWindowQuotaRepository) SyncAccountMembers(ctx context.Context, accountID int64, userIDs []int64, ceiling5h, ceiling7d float64) ([]service.AccountWindowEqualizationResult, error) {
	var results []service.AccountWindowEqualizationResult
	err := r.withWindowQuotaTx(ctx, func(txCtx context.Context, client *dbent.Client) error {
		rows, err := client.QueryContext(txCtx, `SELECT COUNT(*) FROM accounts WHERE id = $1 AND deleted_at IS NULL AND COALESCE(extra->>'window_quota_shared', 'false') = 'true'`, accountID)
		if err != nil {
			return fmt.Errorf("check account %d before syncing members: %w", accountID, err)
		}
		var accountCount int
		if rows.Next() {
			err = rows.Scan(&accountCount)
		}
		_ = rows.Close()
		if err != nil {
			return fmt.Errorf("scan account %d before syncing members: %w", accountID, err)
		}
		if accountCount != 1 {
			return fmt.Errorf("%w: account %d does not exist", service.ErrAccountWindowInvalidMembers, accountID)
		}

		placeholders := make([]string, len(userIDs))
		userArgs := make([]any, len(userIDs))
		for i, userID := range userIDs {
			placeholders[i] = fmt.Sprintf("$%d", i+1)
			userArgs[i] = userID
		}
		usersQ := `SELECT COUNT(*) FROM users WHERE deleted_at IS NULL AND id IN (` + strings.Join(placeholders, ",") + `)`
		rows, err = client.QueryContext(txCtx, usersQ, userArgs...)
		if err != nil {
			return fmt.Errorf("validate account %d members: %w", accountID, err)
		}
		var userCount int
		if rows.Next() {
			err = rows.Scan(&userCount)
		}
		_ = rows.Close()
		if err != nil {
			return fmt.Errorf("scan account %d member validation: %w", accountID, err)
		}
		if userCount != len(userIDs) {
			return fmt.Errorf("%w: one or more selected users do not exist", service.ErrAccountWindowInvalidMembers)
		}

		type windowUsageState struct {
			total   float64
			resetAt *time.Time
		}
		usageByWindow := map[string]windowUsageState{}
		usageRows, err := client.QueryContext(txCtx, `SELECT window_type, COALESCE(SUM(attributed_percent), 0), MAX(window_reset_at)
			FROM user_account_window_quotas
			WHERE account_id = $1 AND deleted_at IS NULL
			GROUP BY window_type`, accountID)
		if err != nil {
			return fmt.Errorf("read existing account %d usage before syncing members: %w", accountID, err)
		}
		for usageRows.Next() {
			var window string
			var total float64
			var resetAt sql.NullTime
			if err := usageRows.Scan(&window, &total, &resetAt); err != nil {
				_ = usageRows.Close()
				return fmt.Errorf("scan existing account %d usage: %w", accountID, err)
			}
			state := windowUsageState{total: total}
			if resetAt.Valid {
				t := resetAt.Time
				state.resetAt = &t
			}
			usageByWindow[window] = state
		}
		if err := usageRows.Err(); err != nil {
			_ = usageRows.Close()
			return fmt.Errorf("iterate existing account %d usage: %w", accountID, err)
		}
		_ = usageRows.Close()

		now := time.Now()
		removeArgs := make([]any, 0, len(userIDs)+2)
		removeArgs = append(removeArgs, accountID, now)
		removePlaceholders := make([]string, len(userIDs))
		for i, userID := range userIDs {
			removePlaceholders[i] = fmt.Sprintf("$%d", i+3)
			removeArgs = append(removeArgs, userID)
		}
		removeQ := `UPDATE user_account_window_quotas SET deleted_at = $2, updated_at = $2
			WHERE account_id = $1 AND deleted_at IS NULL AND user_id NOT IN (` + strings.Join(removePlaceholders, ",") + `)`
		if _, err := client.ExecContext(txCtx, removeQ, removeArgs...); err != nil {
			return fmt.Errorf("remove deselected members from account %d: %w", accountID, err)
		}

		for _, item := range []struct {
			window  string
			ceiling float64
		}{
			{window: service.WindowType5h, ceiling: ceiling5h},
			{window: service.WindowType7d, ceiling: ceiling7d},
		} {
			share := math.Floor(item.ceiling/float64(len(userIDs))*10000) / 10000
			usageState := usageByWindow[item.window]
			usedShare := math.Floor(usageState.total/float64(len(userIDs))*10000) / 10000
			const upsertQ = `INSERT INTO user_account_window_quotas
				(user_id, account_id, window_type, limit_percent, attributed_percent, window_reset_at, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
				ON CONFLICT (user_id, account_id, window_type) WHERE deleted_at IS NULL DO UPDATE SET
					limit_percent = EXCLUDED.limit_percent,
					attributed_percent = EXCLUDED.attributed_percent,
					window_reset_at = COALESCE(EXCLUDED.window_reset_at, user_account_window_quotas.window_reset_at),
					updated_at = EXCLUDED.updated_at`
			for _, userID := range userIDs {
				if _, err := client.ExecContext(txCtx, upsertQ, userID, accountID, item.window, share, usedShare, nullableTime(usageState.resetAt), now); err != nil {
					return fmt.Errorf("add account %d member %d window %s: %w", accountID, userID, item.window, err)
				}
			}
			results = append(results, service.AccountWindowEqualizationResult{
				WindowType:        item.window,
				ActiveMemberCount: len(userIDs),
				SharePercent:      share,
				CeilingPercent:    item.ceiling,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

func (r *userAccountWindowQuotaRepository) withWindowQuotaTx(ctx context.Context, fn func(context.Context, *dbent.Client) error) error {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return fn(ctx, tx.Client())
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin account window quota transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	if err := fn(txCtx, tx.Client()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit account window quota transaction: %w", err)
	}
	return nil
}

// nullableTime 把 *time.Time 转为 ExecContext 可识别的参数（nil → SQL NULL）。
func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}

func entWindowQuotaToRecord(e *dbent.UserAccountWindowQuota) *service.UserAccountWindowQuotaRecord {
	return &service.UserAccountWindowQuotaRecord{
		UserID:            e.UserID,
		AccountID:         e.AccountID,
		WindowType:        e.WindowType,
		LimitPercent:      e.LimitPercent,
		AttributedPercent: e.AttributedPercent,
		WindowResetAt:     e.WindowResetAt,
	}
}
