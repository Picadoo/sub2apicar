package repository

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/useraccountwindowquota"
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

// RecomputeWindowShares 按"本窗口内各用户实际 token 占比"重算该账号该窗口下所有有用量用户的
// attributed_percent = officialPct × (该用户 token / 全部 token)。
// 幂等：从 usage_logs 权威聚合重算（不累加、不受调用次数影响），attributed_percent 为绝对 SET。
// 仅更新 windowStart 之后有用量的用户；total=0 时不更新（无可分摊用量）。
func (r *userAccountWindowQuotaRepository) RecomputeWindowShares(ctx context.Context, accountID int64, window string, officialPct float64, windowStart time.Time, resetAt *time.Time, defaultLimit float64) error {
	client := clientFromContext(ctx, r.client)

	const aggQ = `SELECT user_id,
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0)::bigint AS toks
		FROM usage_logs
		WHERE account_id = $1 AND user_id IS NOT NULL AND created_at >= $2
		GROUP BY user_id`
	rows, err := client.QueryContext(ctx, aggQ, accountID, windowStart)
	if err != nil {
		return err
	}
	type userToks struct {
		userID int64
		toks   int64
	}
	var list []userToks
	var total int64
	for rows.Next() {
		var ut userToks
		if err := rows.Scan(&ut.userID, &ut.toks); err != nil {
			_ = rows.Close()
			return err
		}
		if ut.toks > 0 {
			list = append(list, ut)
			total += ut.toks
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	if total <= 0 {
		return nil
	}

	const upsert = `INSERT INTO user_account_window_quotas
		(user_id, account_id, window_type, limit_percent, attributed_percent, window_reset_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
		ON CONFLICT (user_id, account_id, window_type) WHERE deleted_at IS NULL DO UPDATE SET
			attributed_percent = EXCLUDED.attributed_percent,
			window_reset_at    = COALESCE(EXCLUDED.window_reset_at, user_account_window_quotas.window_reset_at),
			updated_at         = EXCLUDED.updated_at`
	now := time.Now()
	for _, ut := range list {
		pct := officialPct * float64(ut.toks) / float64(total)
		if _, err := client.ExecContext(ctx, upsert, ut.userID, accountID, window, defaultLimit, pct, nullableTime(resetAt), now); err != nil {
			return err
		}
	}
	return nil
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

// GetByUserAccountWindow 查询单条配额（排除软删除）。未找到返回 (nil, nil)。
func (r *userAccountWindowQuotaRepository) GetByUserAccountWindow(ctx context.Context, userID, accountID int64, window string) (*service.UserAccountWindowQuotaRecord, error) {
	client := clientFromContext(ctx, r.client)
	entity, err := client.UserAccountWindowQuota.Query().
		Where(
			useraccountwindowquota.UserIDEQ(userID),
			useraccountwindowquota.AccountIDEQ(accountID),
			useraccountwindowquota.WindowTypeEQ(window),
			useraccountwindowquota.DeletedAtIsNil(),
		).
		Only(ctx)
	if dbent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return entWindowQuotaToRecord(entity), nil
}

// ListByUser 返回用户在所有账号所有窗口的活跃配额（裸 SQL 以带出 donate_pool_fraction）。
func (r *userAccountWindowQuotaRepository) ListByUser(ctx context.Context, userID int64) ([]service.UserAccountWindowQuotaRecord, error) {
	client := clientFromContext(ctx, r.client)
	const q = `SELECT q.user_id, q.account_id, q.window_type, q.limit_percent, q.attributed_percent, q.window_reset_at, COALESCE(q.donate_pool_fraction, 0)
		FROM user_account_window_quotas q
		JOIN accounts a ON a.id = q.account_id
		WHERE q.user_id = $1 AND q.deleted_at IS NULL AND a.deleted_at IS NULL`
	rows, err := client.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanWindowQuotaRecords(rows)
}

// ListByAccount 返回某账号下所有用户所有窗口的活跃配额（裸 SQL 以带出 donate_pool_fraction）。
func (r *userAccountWindowQuotaRepository) ListByAccount(ctx context.Context, accountID int64) ([]service.UserAccountWindowQuotaRecord, error) {
	client := clientFromContext(ctx, r.client)
	const q = `SELECT q.user_id, q.account_id, q.window_type, q.limit_percent, q.attributed_percent, q.window_reset_at, COALESCE(q.donate_pool_fraction, 0)
		FROM user_account_window_quotas q
		JOIN accounts a ON a.id = q.account_id
		WHERE q.account_id = $1 AND q.deleted_at IS NULL AND a.deleted_at IS NULL`
	rows, err := client.QueryContext(ctx, q, accountID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanWindowQuotaRecords(rows)
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

// SetDonatePoolFraction 设置（或新建）某 (user, account, window) 的救急池捐赠比例（∈[0,1]）。
// 5h、7d 各自独立；ON CONFLICT 不覆盖 limit_percent / attributed_percent（仅改捐赠比例）。
func (r *userAccountWindowQuotaRepository) SetDonatePoolFraction(ctx context.Context, userID, accountID int64, window string, fraction, defaultLimit float64) error {
	client := clientFromContext(ctx, r.client)
	const q = `INSERT INTO user_account_window_quotas
		(user_id, account_id, window_type, limit_percent, attributed_percent, donate_pool_fraction, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 0, $5, $6, $6)
		ON CONFLICT (user_id, account_id, window_type) WHERE deleted_at IS NULL DO UPDATE SET
			donate_pool_fraction = EXCLUDED.donate_pool_fraction,
			updated_at           = EXCLUDED.updated_at`
	_, err := client.ExecContext(ctx, q, userID, accountID, window, defaultLimit, fraction, time.Now())
	return err
}

// ListDueResets 返回 window_reset_at 已到期且仍有用量的 (账号, 窗口) 去重列表。
func (r *userAccountWindowQuotaRepository) ListDueResets(ctx context.Context, now time.Time) ([]service.AccountWindowReset, error) {
	client := clientFromContext(ctx, r.client)
	const q = `SELECT DISTINCT q.account_id, q.window_type
		FROM user_account_window_quotas q
		JOIN accounts a ON a.id = q.account_id
		WHERE q.deleted_at IS NULL AND a.deleted_at IS NULL AND q.attributed_percent > 0
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
		rows, err := client.QueryContext(txCtx, `SELECT COUNT(*) FROM accounts WHERE id = $1 AND deleted_at IS NULL`, accountID)
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
			const upsertQ = `INSERT INTO user_account_window_quotas
				(user_id, account_id, window_type, limit_percent, attributed_percent, created_at, updated_at)
				VALUES ($1, $2, $3, $4, 0, $5, $5)
				ON CONFLICT (user_id, account_id, window_type) WHERE deleted_at IS NULL DO UPDATE SET
					limit_percent = EXCLUDED.limit_percent,
					updated_at = EXCLUDED.updated_at`
			for _, userID := range userIDs {
				if _, err := client.ExecContext(txCtx, upsertQ, userID, accountID, item.window, share, now); err != nil {
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
