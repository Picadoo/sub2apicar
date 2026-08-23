package repository

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
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

var errAccountWindowStaleResetBoundary = errors.New("account window reset boundary is newer than candidate checkpoint")

// NewUserAccountWindowQuotaRepository 创建 service.UserAccountWindowQuotaRepository 实现（DI Strategy A：直接返回接口）。
func NewUserAccountWindowQuotaRepository(client *dbent.Client) service.UserAccountWindowQuotaRepository {
	return &userAccountWindowQuotaRepository{client: client}
}

// ApplyWindowSharesFromCheckpoint atomically advances the official snapshot,
// ingests only unseen usage-log cost basis, and settles only the new official delta.
func (r *userAccountWindowQuotaRepository) ApplyWindowSharesFromCheckpoint(
	ctx context.Context,
	accountID int64,
	window string,
	candidate service.AccountWindowAttributionCheckpoint,
	resetAt *time.Time,
) (service.AccountWindowAttributionCheckpoint, bool, error) {
	if !service.IsValidAccountWindowAttributionCheckpoint(&candidate) {
		return service.AccountWindowAttributionCheckpoint{}, false, fmt.Errorf("invalid account window attribution checkpoint")
	}
	if resetAt != nil {
		value := *resetAt
		candidate.ResetAt = &value
	}
	winner := service.CloneAccountWindowAttributionCheckpoint(candidate)
	applied := false
	err := r.withWindowQuotaTx(ctx, func(txCtx context.Context, client *dbent.Client) error {
		checkpointKey := service.AccountWindowAttributionCheckpointExtraKey(window)
		rows, err := client.QueryContext(txCtx, `SELECT extra->>$2,
			extra->>'codex_usage_observed_unix_nano',
			extra->>'codex_usage_updated_at',
			COALESCE(extra->>$3, 'false') = 'true'
			FROM accounts
			WHERE id = $1 AND deleted_at IS NULL
			  AND COALESCE(extra->>'window_quota_shared', 'false') = 'true'
			FOR UPDATE`, accountID, checkpointKey, service.AccountWindowForceUnattributedExtraKey)
		if err != nil {
			return fmt.Errorf("lock account %d attribution checkpoint: %w", accountID, err)
		}
		var raw, canonicalObservedNanoRaw, canonicalObservedAtRaw sql.NullString
		var forceUnattributed bool
		if rows.Next() {
			err = rows.Scan(&raw, &canonicalObservedNanoRaw, &canonicalObservedAtRaw, &forceUnattributed)
		} else if rows.Err() != nil {
			err = rows.Err()
		} else {
			_ = rows.Close()
			return nil
		}
		_ = rows.Close()
		if err != nil {
			return err
		}

		members, err := lockAccountWindowMembersInTx(txCtx, client, accountID, window)
		if err != nil {
			return err
		}

		var decodedRaw *service.AccountWindowAttributionCheckpoint
		var existing *service.AccountWindowAttributionCheckpoint
		if raw.Valid {
			var decoded service.AccountWindowAttributionCheckpoint
			if json.Unmarshal([]byte(raw.String), &decoded) == nil {
				decodedRaw = &decoded
				if service.IsValidAccountWindowAttributionCheckpoint(&decoded) {
					cloned := service.CloneAccountWindowAttributionCheckpoint(decoded)
					existing = &cloned
				}
			}
		}

		canonicalObservedAt := parseAccountOfficialObservedAt(canonicalObservedNanoRaw, canonicalObservedAtRaw)
		candidateFresh := !candidate.CandidateExpired &&
			(canonicalObservedAt.IsZero() || !candidate.LatestObservedAt.Before(canonicalObservedAt))

		if existing == nil {
			if !candidateFresh || membersHaveNewerResetBoundary(members, candidate.ResetAt) {
				return nil
			}
			upper, err := attributionUpperBoundInTx(txCtx, client, accountID, candidate.WindowStart, candidate.ResetAt, candidate.LatestObservedAt, candidate.UsageLog)
			if err != nil {
				return err
			}
			sameWindow := candidate.ResetAt == nil || legacyCheckpointMatchesCandidate(decodedRaw, candidate) || membersMatchCandidateWindow(members, candidate.ResetAt)
			if !sameWindow {
				if err := resetAccountWindowMembersInTx(txCtx, client, accountID, window, candidate.ResetAt, window == service.WindowType5h); err != nil {
					return err
				}
				for i := range members {
					members[i].attributed = 0
					members[i].resetAt = candidate.ResetAt
				}
			} else if candidate.ResetAt != nil {
				if err := updateAccountWindowResetBoundaryInTx(txCtx, client, accountID, window, candidate.ResetAt); err != nil {
					return err
				}
			}
			memberTotal, err := reconcileFrozenMemberTotalInTx(txCtx, client, members, candidate.LatestOfficialPercent)
			if err != nil {
				return err
			}
			winner = service.CloneAccountWindowAttributionCheckpoint(candidate)
			winner.UsageLog = nil
			winner.CandidateExpired = false
			winner.PendingBasisByUser = make(map[int64]float64)
			winner.PendingUnattributedBasisUSD = 0
			winner.UnattributedPercent = roundWindowPercent(math.Max(0, winner.LatestOfficialPercent-memberTotal))
			winner.SeenThroughCreatedAt = upper.createdAt
			winner.SeenThroughUsageLogID = upper.id
			if err := persistAccountWindowCheckpointInTx(txCtx, client, accountID, checkpointKey, winner); err != nil {
				return err
			}
			applied = true
			return nil
		}

		current := service.CloneAccountWindowAttributionCheckpoint(*existing)
		winner = service.CloneAccountWindowAttributionCheckpoint(current)
		accepted := false
		if candidateFresh {
			var selected service.AccountWindowAttributionCheckpoint
			selected, accepted = service.SelectAccountWindowAttributionCheckpoint(existing, candidate, candidate.ResetAt != nil)
			if accepted {
				winner.LatestOfficialPercent = selected.LatestOfficialPercent
				winner.LatestObservedAt = selected.LatestObservedAt
				winner.WindowStart = selected.WindowStart
				winner.ResetAt = selected.ResetAt
				winner.At = selected.At
			}
		}

		newWindow := accepted && !sameRepositoryWindow(current, winner)
		if newWindow {
			upper, err := attributionUpperBoundInTx(txCtx, client, accountID, winner.WindowStart, winner.ResetAt, winner.LatestObservedAt, candidate.UsageLog)
			if err != nil {
				return err
			}
			if err := resetAccountWindowMembersInTx(txCtx, client, accountID, window, winner.ResetAt, window == service.WindowType5h); err != nil {
				return err
			}
			winner.PendingBasisByUser = make(map[int64]float64)
			winner.PendingUnattributedBasisUSD = 0
			winner.UnattributedPercent = roundWindowPercent(winner.LatestOfficialPercent)
			winner.SeenThroughCreatedAt = upper.createdAt
			winner.SeenThroughUsageLogID = upper.id
			winner.UsageLog = nil
			winner.CandidateExpired = false
			if err := persistAccountWindowCheckpointInTx(txCtx, client, accountID, checkpointKey, winner); err != nil {
				return err
			}
			applied = true
			return nil
		}

		resetBoundaryChanged := accepted && !sameOptionalTime(winner.ResetAt, current.ResetAt)
		if resetBoundaryChanged {
			if err := updateAccountWindowResetBoundaryInTx(txCtx, client, accountID, window, winner.ResetAt); err != nil {
				return err
			}
		}
		changed := accepted && (!winner.LatestObservedAt.Equal(current.LatestObservedAt) || winner.LatestOfficialPercent != current.LatestOfficialPercent || resetBoundaryChanged)
		if winner.PendingBasisByUser == nil {
			winner.PendingBasisByUser = make(map[int64]float64)
		}
		moveInactivePendingToUnattributed(&winner, members)

		upper, hasUpper, lateInserted, err := incrementalAttributionUpperBoundInTx(txCtx, client, accountID, current, candidate)
		if err != nil {
			return err
		}
		if forceUnattributed {
			// 账号存在中转站外部消费时，官方增量与本地 usage log 之间没有可证明的因果关系。
			// 因此只推进游标，丢弃历史 pending basis，并将全部官方增量记为未归因。
			if len(winner.PendingBasisByUser) > 0 || winner.PendingUnattributedBasisUSD != 0 {
				// 审计：被丢弃的 pending basis 对应真实已发生的美元消耗，force 模式下不再归因，
				// 但必须留下可追溯日志，避免账面对不上时无从查起。
				droppedTotal := math.Max(0, winner.PendingUnattributedBasisUSD)
				for _, cost := range winner.PendingBasisByUser {
					if cost > 0 && !math.IsNaN(cost) && !math.IsInf(cost, 0) {
						droppedTotal += cost
					}
				}
				slog.Warn("account_window_quota.force_drop_pending_basis",
					"account_id", accountID,
					"window", window,
					"dropped_basis_usd", droppedTotal,
					"pending_members", len(winner.PendingBasisByUser),
				)
				winner.PendingBasisByUser = make(map[int64]float64)
				winner.PendingUnattributedBasisUSD = 0
				changed = true
			}
			if hasUpper && cursorAfter(upper, attributionCursor{createdAt: winner.SeenThroughCreatedAt, id: winner.SeenThroughUsageLogID}) {
				winner.SeenThroughCreatedAt = upper.createdAt
				winner.SeenThroughUsageLogID = upper.id
				changed = true
			}
			deltaOfficial := roundWindowPercent(math.Max(0, winner.LatestOfficialPercent-current.LatestOfficialPercent))
			if deltaOfficial > 0 {
				winner.UnattributedPercent = roundWindowPercent(winner.UnattributedPercent + deltaOfficial)
				changed = true
			}
			if !changed {
				winner = current
				return nil
			}
			winner.UsageLog = nil
			winner.CandidateExpired = false
			if err := persistAccountWindowCheckpointInTx(txCtx, client, accountID, checkpointKey, winner); err != nil {
				return err
			}
			applied = true
			return nil
		}
		if lateInserted && candidate.UsageLog != nil {
			cost := candidate.UsageLog.CostUSD
			if cost > 0 && !math.IsNaN(cost) && !math.IsInf(cost, 0) {
				winner.PendingUnattributedBasisUSD += cost
				changed = true
			}
		}
		if hasUpper && cursorAfter(upper, attributionCursor{createdAt: winner.SeenThroughCreatedAt, id: winner.SeenThroughUsageLogID}) {
			costByUser, err := readIncrementalUsageCostsInTx(txCtx, client, accountID, current.WindowStart, current.ResetAt, attributionCursor{createdAt: winner.SeenThroughCreatedAt, id: winner.SeenThroughUsageLogID}, upper)
			if err != nil {
				return err
			}
			activeUsers := make(map[int64]struct{}, len(members))
			for _, member := range members {
				activeUsers[member.userID] = struct{}{}
			}
			for userID, cost := range costByUser {
				if cost <= 0 || math.IsNaN(cost) || math.IsInf(cost, 0) {
					continue
				}
				if _, ok := activeUsers[userID]; ok {
					winner.PendingBasisByUser[userID] += cost
				} else {
					winner.PendingUnattributedBasisUSD += cost
				}
			}
			winner.SeenThroughCreatedAt = upper.createdAt
			winner.SeenThroughUsageLogID = upper.id
			changed = true
		}

		deltaOfficial := roundWindowPercent(math.Max(0, winner.LatestOfficialPercent-current.LatestOfficialPercent))
		allocatedToMembers := 0.0
		if deltaOfficial > 0 {
			unattributedDelta, err := settlePendingOfficialDeltaInTx(txCtx, client, members, &winner, deltaOfficial)
			if err != nil {
				return err
			}
			// settle 返回的是未分完的余量；本次实际分给成员的份额 = delta − 余量。
			allocatedToMembers = roundWindowPercent(math.Max(0, deltaOfficial-unattributedDelta))
			winner.UnattributedPercent = roundWindowPercent(winner.UnattributedPercent + unattributedDelta)
			changed = true
		}
		// 重锚：非 force 模式下，checkpoint 的未归因必须与「官方 − 成员归因(结算后)」保持一致。
		// 这修复 force→非force 切换后历史 UnattributedPercent 只增不减、与成员归因双计的问题。
		// 仅在已有变更（changed）的写路径上重锚；纯只读/拒绝场景（changed=false）不得因重锚变成写操作。
		if changed {
			memberTotal := allocatedToMembers
			for _, member := range members {
				if member.attributed > 0 && !math.IsNaN(member.attributed) && !math.IsInf(member.attributed, 0) {
					memberTotal += member.attributed
				}
			}
			winner.UnattributedPercent = roundWindowPercent(math.Max(0, winner.LatestOfficialPercent-memberTotal))
		}
		if !changed {
			winner = current
			return nil
		}
		winner.UsageLog = nil
		winner.CandidateExpired = false
		if err := persistAccountWindowCheckpointInTx(txCtx, client, accountID, checkpointKey, winner); err != nil {
			return err
		}
		applied = true
		return nil
	})
	return winner, applied, err
}

type accountWindowMemberState struct {
	id         int64
	userID     int64
	attributed float64
	resetAt    *time.Time
}

type attributionCursor struct {
	createdAt time.Time
	id        int64
}

func lockAccountWindowMembersInTx(ctx context.Context, client *dbent.Client, accountID int64, window string) ([]accountWindowMemberState, error) {
	const q = `SELECT q.id, q.user_id, q.attributed_percent, q.window_reset_at
		FROM user_account_window_quotas q
		WHERE q.account_id = $1 AND q.window_type = $2 AND q.deleted_at IS NULL
		ORDER BY q.user_id
		FOR UPDATE`
	rows, err := client.QueryContext(ctx, q, accountID, window)
	if err != nil {
		return nil, fmt.Errorf("lock account %d window %s members: %w", accountID, window, err)
	}
	defer rows.Close()
	members := make([]accountWindowMemberState, 0)
	for rows.Next() {
		var member accountWindowMemberState
		var resetAt sql.NullTime
		if err := rows.Scan(&member.id, &member.userID, &member.attributed, &resetAt); err != nil {
			return nil, fmt.Errorf("scan account %d window %s member: %w", accountID, window, err)
		}
		if resetAt.Valid {
			value := resetAt.Time
			member.resetAt = &value
		}
		members = append(members, member)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate account %d window %s members: %w", accountID, window, err)
	}
	return members, nil
}

func roundWindowPercent(value float64) float64 {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return math.Round(value*10000) / 10000
}

func sameOptionalTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return service.AccountWindowBoundariesMatch(*a, *b)
}

func sameRepositoryWindow(a, b service.AccountWindowAttributionCheckpoint) bool {
	if a.ResetAt != nil && b.ResetAt != nil {
		return sameOptionalTime(a.ResetAt, b.ResetAt)
	}
	return service.AccountWindowBoundariesMatch(a.WindowStart, b.WindowStart)
}

func legacyCheckpointMatchesCandidate(legacy *service.AccountWindowAttributionCheckpoint, candidate service.AccountWindowAttributionCheckpoint) bool {
	if legacy == nil {
		return false
	}
	if legacy.ResetAt != nil && candidate.ResetAt != nil {
		return sameOptionalTime(legacy.ResetAt, candidate.ResetAt)
	}
	return service.AccountWindowBoundariesMatch(legacy.WindowStart, candidate.WindowStart)
}

func membersMatchCandidateWindow(members []accountWindowMemberState, resetAt *time.Time) bool {
	if resetAt == nil {
		return false
	}
	for _, member := range members {
		if member.resetAt != nil && sameOptionalTime(member.resetAt, resetAt) {
			return true
		}
	}
	return false
}

func membersHaveNewerResetBoundary(members []accountWindowMemberState, resetAt *time.Time) bool {
	if resetAt == nil {
		return false
	}
	for _, member := range members {
		if member.resetAt != nil && member.resetAt.After(resetAt.Add(service.AccountWindowBoundaryTolerance)) {
			return true
		}
	}
	return false
}

func cursorAfter(candidate, current attributionCursor) bool {
	if candidate.createdAt.IsZero() {
		return false
	}
	if current.createdAt.IsZero() {
		return true
	}
	if candidate.createdAt.After(current.createdAt) {
		return true
	}
	return candidate.createdAt.Equal(current.createdAt) && candidate.id > current.id
}

func cursorWithinWindow(cursor attributionCursor, windowStart time.Time, resetAt *time.Time) bool {
	if cursor.createdAt.IsZero() || cursor.createdAt.Before(windowStart) {
		return false
	}
	return resetAt == nil || cursor.createdAt.Before(*resetAt)
}

func usageLogCursorInTx(ctx context.Context, client *dbent.Client, accountID int64, ref *service.AccountWindowUsageLogRef) (attributionCursor, bool, error) {
	if ref == nil {
		return attributionCursor{}, false, nil
	}
	if ref.ID > 0 && !ref.CreatedAt.IsZero() {
		return attributionCursor{createdAt: ref.CreatedAt, id: ref.ID}, true, nil
	}
	var q string
	var args []any
	if ref.ID > 0 {
		q = `SELECT created_at, id FROM usage_logs WHERE account_id = $1 AND id = $2`
		args = []any{accountID, ref.ID}
	} else if strings.TrimSpace(ref.RequestID) != "" && ref.APIKeyID > 0 {
		q = `SELECT created_at, id FROM usage_logs WHERE account_id = $1 AND request_id = $2 AND api_key_id = $3`
		args = []any{accountID, ref.RequestID, ref.APIKeyID}
	} else {
		return attributionCursor{}, false, nil
	}
	rows, err := client.QueryContext(ctx, q, args...)
	if err != nil {
		return attributionCursor{}, false, fmt.Errorf("resolve account %d usage-log cursor: %w", accountID, err)
	}
	defer rows.Close()
	if !rows.Next() {
		return attributionCursor{}, false, rows.Err()
	}
	var cursor attributionCursor
	if err := rows.Scan(&cursor.createdAt, &cursor.id); err != nil {
		return attributionCursor{}, false, err
	}
	return cursor, true, nil
}

func latestUsageCursorInTx(ctx context.Context, client *dbent.Client, accountID int64, windowStart time.Time, resetAt *time.Time, observedAt time.Time) (attributionCursor, bool, error) {
	const q = `SELECT created_at, id
		FROM usage_logs
		WHERE account_id = $1 AND created_at >= $2
		  AND ($3::timestamptz IS NULL OR created_at < $3)
		  AND created_at <= $4
		ORDER BY created_at DESC, id DESC
		LIMIT 1`
	rows, err := client.QueryContext(ctx, q, accountID, windowStart, nullableTime(resetAt), observedAt)
	if err != nil {
		return attributionCursor{}, false, fmt.Errorf("read account %d latest usage cursor: %w", accountID, err)
	}
	defer rows.Close()
	if !rows.Next() {
		return attributionCursor{}, false, rows.Err()
	}
	var cursor attributionCursor
	if err := rows.Scan(&cursor.createdAt, &cursor.id); err != nil {
		return attributionCursor{}, false, err
	}
	return cursor, true, nil
}

func attributionUpperBoundInTx(ctx context.Context, client *dbent.Client, accountID int64, windowStart time.Time, resetAt *time.Time, observedAt time.Time, ref *service.AccountWindowUsageLogRef) (attributionCursor, error) {
	if cursor, ok, err := usageLogCursorInTx(ctx, client, accountID, ref); err != nil {
		return attributionCursor{}, err
	} else if ok && cursorWithinWindow(cursor, windowStart, resetAt) {
		return cursor, nil
	}
	cursor, ok, err := latestUsageCursorInTx(ctx, client, accountID, windowStart, resetAt, observedAt)
	if err != nil || !ok {
		return attributionCursor{}, err
	}
	return cursor, nil
}

func incrementalAttributionUpperBoundInTx(ctx context.Context, client *dbent.Client, accountID int64, current service.AccountWindowAttributionCheckpoint, candidate service.AccountWindowAttributionCheckpoint) (attributionCursor, bool, bool, error) {
	if candidate.UsageLog != nil {
		cursor, ok, err := usageLogCursorInTx(ctx, client, accountID, candidate.UsageLog)
		if err != nil || !ok || !cursorWithinWindow(cursor, current.WindowStart, current.ResetAt) {
			return attributionCursor{}, false, false, err
		}
		if cursorAfter(cursor, attributionCursor{createdAt: current.SeenThroughCreatedAt, id: current.SeenThroughUsageLogID}) {
			return cursor, true, false, nil
		}
		// 真正新插入但游标已落后的日志不能回写历史成员，只能作为未知成本等待后续官方增量结算。
		// inserted=false 表示幂等重试命中的既有日志，必须保持 no-op，避免重复累计。
		return attributionCursor{}, false, candidate.UsageLog.Inserted, nil
	}
	if candidate.CandidateExpired || candidate.LatestObservedAt.Before(current.LatestObservedAt) {
		return attributionCursor{}, false, false, nil
	}
	cursor, ok, err := latestUsageCursorInTx(ctx, client, accountID, current.WindowStart, current.ResetAt, candidate.LatestObservedAt)
	return cursor, ok, false, err
}

func readIncrementalUsageCostsInTx(ctx context.Context, client *dbent.Client, accountID int64, windowStart time.Time, resetAt *time.Time, lower, upper attributionCursor) (map[int64]float64, error) {
	const q = `SELECT user_id,
		COALESCE(SUM(GREATEST(COALESCE(total_cost, 0), 0)), 0)::double precision
		FROM usage_logs
		WHERE account_id = $1 AND created_at >= $2
		  AND ($3::timestamptz IS NULL OR created_at < $3)
		  AND (created_at, id) > ($4::timestamptz, $5::bigint)
		  AND (created_at, id) <= ($6::timestamptz, $7::bigint)
		GROUP BY user_id`
	rows, err := client.QueryContext(ctx, q, accountID, windowStart, nullableTime(resetAt), lower.createdAt, lower.id, upper.createdAt, upper.id)
	if err != nil {
		return nil, fmt.Errorf("read account %d incremental usage costs: %w", accountID, err)
	}
	defer rows.Close()
	costByUser := make(map[int64]float64)
	for rows.Next() {
		var userID int64
		var cost float64
		if err := rows.Scan(&userID, &cost); err != nil {
			return nil, err
		}
		costByUser[userID] = cost
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return costByUser, nil
}

func moveInactivePendingToUnattributed(checkpoint *service.AccountWindowAttributionCheckpoint, members []accountWindowMemberState) {
	if checkpoint == nil || len(checkpoint.PendingBasisByUser) == 0 {
		return
	}
	active := make(map[int64]struct{}, len(members))
	for _, member := range members {
		active[member.userID] = struct{}{}
	}
	for userID, cost := range checkpoint.PendingBasisByUser {
		if _, ok := active[userID]; ok {
			continue
		}
		if cost > 0 && !math.IsNaN(cost) && !math.IsInf(cost, 0) {
			checkpoint.PendingUnattributedBasisUSD += cost
		}
		delete(checkpoint.PendingBasisByUser, userID)
	}
}

func settlePendingOfficialDeltaInTx(ctx context.Context, client *dbent.Client, members []accountWindowMemberState, checkpoint *service.AccountWindowAttributionCheckpoint, deltaOfficial float64) (float64, error) {
	deltaOfficial = roundWindowPercent(deltaOfficial)
	if checkpoint == nil || deltaOfficial <= 0 {
		return 0, nil
	}
	totalBasis := math.Max(0, checkpoint.PendingUnattributedBasisUSD)
	for _, cost := range checkpoint.PendingBasisByUser {
		if cost > 0 && !math.IsNaN(cost) && !math.IsInf(cost, 0) {
			totalBasis += cost
		}
	}
	if totalBasis <= 0 {
		checkpoint.PendingBasisByUser = make(map[int64]float64)
		checkpoint.PendingUnattributedBasisUSD = 0
		return deltaOfficial, nil
	}

	allocated := 0.0
	const updateQ = `UPDATE user_account_window_quotas
		SET attributed_percent = attributed_percent + $2, updated_at = $3
		WHERE id = $1 AND deleted_at IS NULL`
	now := time.Now()
	for _, member := range members {
		cost := checkpoint.PendingBasisByUser[member.userID]
		if cost <= 0 || math.IsNaN(cost) || math.IsInf(cost, 0) {
			continue
		}
		increment := math.Floor(deltaOfficial*cost/totalBasis*10000) / 10000
		if increment <= 0 {
			continue
		}
		if _, err := client.ExecContext(ctx, updateQ, member.id, increment, now); err != nil {
			return 0, fmt.Errorf("increment account window attribution for user %d: %w", member.userID, err)
		}
		allocated += increment
	}
	checkpoint.PendingBasisByUser = make(map[int64]float64)
	checkpoint.PendingUnattributedBasisUSD = 0
	return roundWindowPercent(math.Max(0, deltaOfficial-allocated)), nil
}

func resetAccountWindowMembersInTx(ctx context.Context, client *dbent.Client, accountID int64, window string, resetAt *time.Time, clearDonation bool) error {
	const q = `UPDATE user_account_window_quotas
		SET attributed_percent = 0,
			donate_pool_fraction = CASE WHEN $4 THEN 0 ELSE donate_pool_fraction END,
			window_reset_at = $3,
			updated_at = $5
		WHERE account_id = $1 AND window_type = $2 AND deleted_at IS NULL`
	_, err := client.ExecContext(ctx, q, accountID, window, nullableTime(resetAt), clearDonation, time.Now())
	if err != nil {
		return fmt.Errorf("reset account %d window %s for new boundary: %w", accountID, window, err)
	}
	return nil
}

func updateAccountWindowResetBoundaryInTx(ctx context.Context, client *dbent.Client, accountID int64, window string, resetAt *time.Time) error {
	if resetAt == nil {
		return nil
	}
	const q = `UPDATE user_account_window_quotas
		SET window_reset_at = $3, updated_at = $4
		WHERE account_id = $1 AND window_type = $2 AND deleted_at IS NULL`
	_, err := client.ExecContext(ctx, q, accountID, window, *resetAt, time.Now())
	return err
}

func reconcileFrozenMemberTotalInTx(ctx context.Context, client *dbent.Client, members []accountWindowMemberState, officialPercent float64) (float64, error) {
	total := 0.0
	for _, member := range members {
		if member.attributed > 0 && !math.IsNaN(member.attributed) && !math.IsInf(member.attributed, 0) {
			total += member.attributed
		}
	}
	officialPercent = roundWindowPercent(officialPercent)
	if total <= officialPercent+1e-9 {
		return roundWindowPercent(total), nil
	}
	factor := 0.0
	if total > 0 {
		factor = officialPercent / total
	}
	adjustedTotal := 0.0
	const q = `UPDATE user_account_window_quotas SET attributed_percent = $2, updated_at = $3 WHERE id = $1 AND deleted_at IS NULL`
	now := time.Now()
	for _, member := range members {
		value := math.Floor(math.Max(0, member.attributed*factor)*10000) / 10000
		if _, err := client.ExecContext(ctx, q, member.id, value, now); err != nil {
			return 0, err
		}
		adjustedTotal += value
	}
	return roundWindowPercent(adjustedTotal), nil
}

func persistAccountWindowCheckpointInTx(ctx context.Context, client *dbent.Client, accountID int64, checkpointKey string, checkpoint service.AccountWindowAttributionCheckpoint) error {
	checkpoint.UsageLog = nil
	checkpoint.CandidateExpired = false
	encodedCheckpoint, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("encode account %d checkpoint: %w", accountID, err)
	}
	payload, err := json.Marshal(map[string]any{checkpointKey: json.RawMessage(encodedCheckpoint)})
	if err != nil {
		return fmt.Errorf("encode account %d checkpoint update: %w", accountID, err)
	}
	result, err := client.ExecContext(ctx, `UPDATE accounts
		SET extra = COALESCE(extra, '{}'::jsonb) || $2::jsonb, updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL`, accountID, string(payload))
	if err != nil {
		return fmt.Errorf("persist account %d checkpoint: %w", accountID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return service.ErrAccountNotFound
	}
	return nil
}

// RecomputeWindowSharesFromCheckpoint is retained for focused repository tests;
// production service paths use ApplyWindowSharesFromCheckpoint for DB fencing.
func (r *userAccountWindowQuotaRepository) RecomputeWindowSharesFromCheckpoint(ctx context.Context, accountID int64, window string, officialPercent float64, windowStart time.Time, resetAt *time.Time) error {
	return r.withWindowQuotaTx(ctx, func(txCtx context.Context, client *dbent.Client) error {
		return recomputeWindowSharesInTx(txCtx, client, accountID, window, officialPercent, windowStart, resetAt)
	})
}

func recomputeWindowSharesInTx(txCtx context.Context, client *dbent.Client, accountID int64, window string, officialPercent float64, windowStart time.Time, resetAt *time.Time) error {
	if math.IsNaN(officialPercent) || math.IsInf(officialPercent, 0) || officialPercent < 0 {
		officialPercent = 0
	}
	const rowsQ = `SELECT q.id, q.user_id, q.window_reset_at
			FROM user_account_window_quotas q
			JOIN accounts a ON a.id = q.account_id
			WHERE q.account_id = $1 AND q.window_type = $2
			  AND q.deleted_at IS NULL AND a.deleted_at IS NULL
			  AND COALESCE(a.extra->>'window_quota_shared', 'false') = 'true'
			ORDER BY q.user_id
			FOR UPDATE`
	rows, err := client.QueryContext(txCtx, rowsQ, accountID, window)
	if err != nil {
		return fmt.Errorf("lock account %d window %s before cost recompute: %w", accountID, window, err)
	}
	type memberUsage struct {
		id     int64
		userID int64
		cost   float64
	}
	var members []memberUsage
	staleResetBoundary := false
	for rows.Next() {
		var member memberUsage
		var currentResetAt sql.NullTime
		if err := rows.Scan(&member.id, &member.userID, &currentResetAt); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan account %d window %s member: %w", accountID, window, err)
		}
		if resetAt != nil && currentResetAt.Valid && currentResetAt.Time.After(resetAt.Add(service.AccountWindowBoundaryTolerance)) {
			staleResetBoundary = true
		}
		members = append(members, member)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate account %d window %s members: %w", accountID, window, err)
	}
	_ = rows.Close()
	if staleResetBoundary {
		return errAccountWindowStaleResetBoundary
	}
	if len(members) == 0 {
		return nil
	}

	placeholders := make([]string, len(members))
	usageArgs := make([]any, 0, len(members)+3)
	usageArgs = append(usageArgs, accountID, windowStart, nullableTime(resetAt))
	for i, member := range members {
		placeholders[i] = fmt.Sprintf("$%d", i+4)
		usageArgs = append(usageArgs, member.userID)
	}
	usageQ := `SELECT user_id,
			COALESCE(SUM(GREATEST(COALESCE(total_cost, 0), 0)), 0)::double precision
			FROM usage_logs
			WHERE account_id = $1 AND created_at >= $2
			  AND ($3::timestamptz IS NULL OR created_at < $3)
			  AND user_id IN (` + strings.Join(placeholders, ",") + `)
			GROUP BY user_id`
	usageRows, err := client.QueryContext(txCtx, usageQ, usageArgs...)
	if err != nil {
		return fmt.Errorf("read account %d window %s costs: %w", accountID, window, err)
	}
	costByUser := make(map[int64]float64, len(members))
	totalCost := 0.0
	for usageRows.Next() {
		var userID int64
		var cost float64
		if err := usageRows.Scan(&userID, &cost); err != nil {
			_ = usageRows.Close()
			return fmt.Errorf("scan account %d window %s costs: %w", accountID, window, err)
		}
		if cost > 0 && !math.IsNaN(cost) && !math.IsInf(cost, 0) {
			costByUser[userID] = cost
			totalCost += cost
		}
	}
	if err := usageRows.Err(); err != nil {
		_ = usageRows.Close()
		return fmt.Errorf("iterate account %d window %s costs: %w", accountID, window, err)
	}
	_ = usageRows.Close()
	for i := range members {
		members[i].cost = costByUser[members[i].userID]
	}

	target := math.Round(officialPercent*10000) / 10000
	rawValues := make([]float64, len(members))
	if totalCost > 0 {
		for i, member := range members {
			rawValues[i] = target * member.cost / totalCost
		}
	}

	// 没有正美元成本时不存在合法的成员权重，全部归因保持为 0。
	// 账号总量仍由官方快照单独保存/展示，绝不均分，也绝不回退 Token。
	remainderIndex := -1
	for i, member := range members {
		if member.cost > 0 {
			remainderIndex = i
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
		value := 0.0
		if totalCost > 0 {
			if i == remainderIndex {
				// 四位小数尾差只落到最后一个正美元权重成员；零成本成员必须严格为 0。
				value = math.Round(remaining*10000) / 10000
			} else {
				value = math.Floor(math.Max(0, rawValues[i])*10000) / 10000
				remaining -= value
			}
			if value < 0 {
				value = 0
			}
		}
		if _, err := client.ExecContext(txCtx, updateQ, member.id, value, nullableTime(resetAt), now); err != nil {
			return fmt.Errorf("write account %d window %s cost share for user %d: %w", accountID, window, member.userID, err)
		}
	}
	return nil
}

// ResetWindowForAccountIfDue 在账号行锁事务内清理到期窗口及其 durable checkpoint。
// 新窗口若已先提交 future boundary，本次条件更新为 no-op；否则 5h donation 必须一并清零。
func (r *userAccountWindowQuotaRepository) ResetWindowForAccountIfDue(ctx context.Context, accountID int64, window string, dueAt time.Time) (bool, error) {
	reset := false
	err := r.withWindowQuotaTx(ctx, func(txCtx context.Context, client *dbent.Client) error {
		rows, err := client.QueryContext(txCtx, `SELECT id FROM accounts
			WHERE id = $1 AND deleted_at IS NULL
			  AND COALESCE(extra->>'window_quota_shared', 'false') = 'true'
			FOR UPDATE`, accountID)
		if err != nil {
			return err
		}
		exists := rows.Next()
		if rows.Err() != nil {
			err = rows.Err()
		}
		_ = rows.Close()
		if err != nil || !exists {
			return err
		}

		const q = `UPDATE user_account_window_quotas AS target
			SET attributed_percent = 0,
				donate_pool_fraction = CASE WHEN $2 = '5h' THEN 0 ELSE target.donate_pool_fraction END,
				window_reset_at = NULL, updated_at = $4
			WHERE target.account_id = $1 AND target.window_type = $2 AND target.deleted_at IS NULL
			  AND EXISTS (
				SELECT 1 FROM user_account_window_quotas AS due
				WHERE due.account_id = $1 AND due.window_type = $2 AND due.deleted_at IS NULL
				  AND due.window_reset_at IS NOT NULL AND due.window_reset_at <= $3
			  )
			  AND NOT EXISTS (
				SELECT 1 FROM user_account_window_quotas AS future
				WHERE future.account_id = $1 AND future.window_type = $2 AND future.deleted_at IS NULL
				  AND future.window_reset_at > $3
			  )`
		result, err := client.ExecContext(txCtx, q, accountID, window, dueAt, time.Now())
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return nil
		}
		checkpointKey := service.AccountWindowAttributionCheckpointExtraKey(window)
		if _, err := client.ExecContext(txCtx, `UPDATE accounts
			SET extra = COALESCE(extra, '{}'::jsonb) - $2, updated_at = NOW()
			WHERE id = $1 AND deleted_at IS NULL`, accountID, checkpointKey); err != nil {
			return fmt.Errorf("clear account %d window %s checkpoint: %w", accountID, window, err)
		}
		reset = true
		return nil
	})
	return reset, err
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

// ListByAccount 返回共享且未删除账号下的活跃配额；历史 private 行不能继续参与网关预检。
func (r *userAccountWindowQuotaRepository) ListByAccount(ctx context.Context, accountID int64) ([]service.UserAccountWindowQuotaRecord, error) {
	const q = `SELECT q.user_id, q.account_id, q.window_type, q.limit_percent, q.attributed_percent, q.window_reset_at,
		COALESCE(q.donate_pool_fraction, 0)
		FROM user_account_window_quotas q
		JOIN accounts a ON a.id = q.account_id
		WHERE q.account_id = $1 AND q.deleted_at IS NULL AND a.deleted_at IS NULL
		  AND COALESCE(a.extra->>'window_quota_shared', 'false') = 'true'
		ORDER BY q.user_id, q.window_type`
	client := clientFromContext(ctx, r.client)
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

// IsAccountWindowForceUnattributed 读取账号是否处于「官方用量强制未归因」模式。
// 该模式标记在 accounts.extra 的 window_quota_force_unattributed 上；查询失败或账号不存在时返回 false。
func (r *userAccountWindowQuotaRepository) IsAccountWindowForceUnattributed(ctx context.Context, accountID int64) (bool, error) {
	const q = `SELECT COALESCE(extra->>$2, 'false') = 'true'
		FROM accounts
		WHERE id = $1 AND deleted_at IS NULL`
	rows, err := r.client.QueryContext(ctx, q, accountID, service.AccountWindowForceUnattributedExtraKey)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	force := false
	if rows.Next() {
		if err := rows.Scan(&force); err != nil {
			return false, err
		}
	}
	return force, rows.Err()
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

// GetAccountOfficialWindowPercent 读取 accounts.extra 中持久化的 OpenAI 官方窗口快照。
// 只有仍在有效期内且带官方 reset 的快照可用于账号级硬闸；不读取本地 usage_logs 或成员归因。
func (r *userAccountWindowQuotaRepository) GetAccountOfficialWindowPercent(ctx context.Context, accountID int64, window string) (float64, bool, error) {
	usedKey := "codex_5h_used_percent"
	resetAtKey := "codex_5h_reset_at"
	legacyResetKey := "codex_5h_reset"
	resetAfterKey := "codex_5h_reset_after_seconds"
	if window == service.WindowType7d {
		usedKey = "codex_7d_used_percent"
		resetAtKey = "codex_7d_reset_at"
		legacyResetKey = "codex_7d_reset"
		resetAfterKey = "codex_7d_reset_after_seconds"
	}
	checkpointKey := service.AccountWindowAttributionCheckpointExtraKey(window)
	const q = `SELECT
		extra->>$2,
		extra->>$3,
		extra->>$4,
		extra->>$5,
		extra->>'codex_usage_updated_at',
		extra->>'codex_usage_observed_unix_nano',
		extra->>$6
		FROM accounts
		WHERE id = $1 AND deleted_at IS NULL`
	rows, err := r.client.QueryContext(ctx, q, accountID, usedKey, resetAtKey, legacyResetKey, resetAfterKey, checkpointKey)
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, false, err
		}
		return 0, false, nil
	}
	var usedRaw, resetAtRaw, legacyResetRaw, resetAfterRaw, observedAtRaw, observedNanoRaw, checkpointRaw sql.NullString
	if err := rows.Scan(&usedRaw, &resetAtRaw, &legacyResetRaw, &resetAfterRaw, &observedAtRaw, &observedNanoRaw, &checkpointRaw); err != nil {
		return 0, false, err
	}
	now := time.Now()
	var checkpoint *service.AccountWindowAttributionCheckpoint
	if checkpointRaw.Valid {
		var decoded service.AccountWindowAttributionCheckpoint
		if json.Unmarshal([]byte(checkpointRaw.String), &decoded) == nil && service.IsValidAccountWindowAttributionCheckpoint(&decoded) {
			checkpointResetAt := decoded.ResetAt
			if checkpointResetAt == nil {
				fallback := decoded.WindowStart.Add(time.Duration(windowLengthSecondsForRepository(window)) * time.Second)
				checkpointResetAt = &fallback
			}
			if checkpointResetAt.After(now) {
				checkpoint = &decoded
			}
		}
	}

	canonicalFound := false
	canonicalUsed := 0.0
	var canonicalResetAt time.Time
	if usedRaw.Valid {
		if used, err := strconv.ParseFloat(usedRaw.String, 64); err == nil && !math.IsNaN(used) && !math.IsInf(used, 0) && used >= 0 {
			if resetAt, foundReset := parseAccountOfficialResetAt(resetAtRaw, legacyResetRaw, resetAfterRaw, observedAtRaw); foundReset && resetAt.After(now) {
				canonicalFound = true
				canonicalUsed = used
				canonicalResetAt = resetAt
			}
		}
	}
	canonicalObservedAt := parseAccountOfficialObservedAt(observedNanoRaw, observedAtRaw)
	if checkpoint != nil && canonicalFound && !canonicalObservedAt.IsZero() {
		candidate := service.NewAccountWindowAttributionCheckpoint(
			canonicalUsed,
			canonicalResetAt.Add(-time.Duration(windowLengthSecondsForRepository(window))*time.Second),
			canonicalObservedAt,
			&canonicalResetAt,
		)
		winner, _ := service.SelectAccountWindowAttributionCheckpoint(checkpoint, candidate, true)
		return winner.LatestOfficialPercent, true, rows.Err()
	}
	if checkpoint != nil {
		return checkpoint.LatestOfficialPercent, true, rows.Err()
	}
	if canonicalFound {
		return canonicalUsed, true, rows.Err()
	}
	return 0, false, rows.Err()
}

func parseAccountOfficialObservedAt(observedNanoRaw, observedAtRaw sql.NullString) time.Time {
	if observedNanoRaw.Valid {
		if observedNano, err := strconv.ParseInt(observedNanoRaw.String, 10, 64); err == nil && observedNano > 0 {
			return time.Unix(0, observedNano).UTC()
		}
	}
	if observedAtRaw.Valid {
		if observedAt, err := time.Parse(time.RFC3339Nano, observedAtRaw.String); err == nil {
			return observedAt
		}
	}
	return time.Time{}
}

func accountExtraString(extra map[string]any, key string) (string, bool) {
	value, ok := extra[key]
	if !ok || value == nil {
		return "", false
	}
	switch typed := value.(type) {
	case string:
		return typed, true
	case json.Number:
		return typed.String(), true
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	default:
		return "", false
	}
}

func accountExtraNullString(extra map[string]any, key string) sql.NullString {
	value, ok := accountExtraString(extra, key)
	return sql.NullString{String: value, Valid: ok}
}

func durableAccountWindowAttributionCheckpoints(raw []byte, now time.Time) map[string]service.AccountWindowAttributionCheckpoint {
	result := make(map[string]service.AccountWindowAttributionCheckpoint, 2)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var extra map[string]any
	if decoder.Decode(&extra) != nil {
		return result
	}
	for _, window := range []string{service.WindowType5h, service.WindowType7d} {
		rawCheckpoint, ok := extra[service.AccountWindowAttributionCheckpointExtraKey(window)]
		if !ok {
			continue
		}
		encoded, err := json.Marshal(rawCheckpoint)
		if err != nil {
			continue
		}
		var checkpoint service.AccountWindowAttributionCheckpoint
		if json.Unmarshal(encoded, &checkpoint) != nil || !service.IsValidAccountWindowAttributionCheckpoint(&checkpoint) {
			continue
		}
		resetAt := checkpoint.ResetAt
		if resetAt == nil {
			fallback := checkpoint.WindowStart.Add(time.Duration(windowLengthSecondsForRepository(window)) * time.Second)
			resetAt = &fallback
		}
		if resetAt.After(now) {
			result[window] = service.CloneAccountWindowAttributionCheckpoint(checkpoint)
		}
	}
	return result
}

func effectiveAccountWindowRecomputeCheckpoints(raw []byte, now time.Time) map[string]service.AccountWindowAttributionCheckpoint {
	result := make(map[string]service.AccountWindowAttributionCheckpoint, 2)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var extra map[string]any
	if decoder.Decode(&extra) != nil {
		return result
	}
	observedAt := parseAccountOfficialObservedAt(
		accountExtraNullString(extra, "codex_usage_observed_unix_nano"),
		accountExtraNullString(extra, "codex_usage_updated_at"),
	)
	for _, window := range []string{service.WindowType5h, service.WindowType7d} {
		var existing *service.AccountWindowAttributionCheckpoint
		if rawCheckpoint, ok := extra[service.AccountWindowAttributionCheckpointExtraKey(window)]; ok {
			if encoded, err := json.Marshal(rawCheckpoint); err == nil {
				var checkpoint service.AccountWindowAttributionCheckpoint
				if json.Unmarshal(encoded, &checkpoint) == nil && service.IsValidAccountWindowAttributionCheckpoint(&checkpoint) {
					existing = &checkpoint
				}
			}
		}

		usedKey, resetAtKey, legacyResetKey, resetAfterKey := "codex_5h_used_percent", "codex_5h_reset_at", "codex_5h_reset", "codex_5h_reset_after_seconds"
		if window == service.WindowType7d {
			usedKey, resetAtKey, legacyResetKey, resetAfterKey = "codex_7d_used_percent", "codex_7d_reset_at", "codex_7d_reset", "codex_7d_reset_after_seconds"
		}
		var candidate *service.AccountWindowAttributionCheckpoint
		if usedRaw, ok := accountExtraString(extra, usedKey); ok && !observedAt.IsZero() {
			if used, err := strconv.ParseFloat(usedRaw, 64); err == nil && used >= 0 && !math.IsNaN(used) && !math.IsInf(used, 0) {
				if resetAt, found := parseAccountOfficialResetAt(
					accountExtraNullString(extra, resetAtKey),
					accountExtraNullString(extra, legacyResetKey),
					accountExtraNullString(extra, resetAfterKey),
					accountExtraNullString(extra, "codex_usage_updated_at"),
				); found && resetAt.After(now) {
					windowStart := resetAt.Add(-time.Duration(windowLengthSecondsForRepository(window)) * time.Second)
					checkpoint := service.NewAccountWindowAttributionCheckpoint(used, windowStart, observedAt, &resetAt)
					candidate = &checkpoint
				}
			}
		}

		winner := existing
		if candidate != nil {
			selected, _ := service.SelectAccountWindowAttributionCheckpoint(existing, *candidate, true)
			winner = &selected
		}
		if winner == nil || !service.IsValidAccountWindowAttributionCheckpoint(winner) {
			continue
		}
		resetAt := winner.ResetAt
		if resetAt == nil {
			fallback := winner.WindowStart.Add(time.Duration(windowLengthSecondsForRepository(window)) * time.Second)
			resetAt = &fallback
		}
		if resetAt.After(now) {
			result[window] = *winner
		}
	}
	return result
}

func parseAccountOfficialResetAt(resetAtRaw, legacyResetRaw, resetAfterRaw, observedAtRaw sql.NullString) (time.Time, bool) {
	if resetAtRaw.Valid {
		if resetAt, err := time.Parse(time.RFC3339Nano, resetAtRaw.String); err == nil {
			return resetAt, true
		}
	}
	if legacyResetRaw.Valid {
		if resetUnix, err := strconv.ParseInt(legacyResetRaw.String, 10, 64); err == nil && resetUnix > 0 {
			return time.Unix(resetUnix, 0), true
		}
		if resetAt, err := time.Parse(time.RFC3339Nano, legacyResetRaw.String); err == nil {
			return resetAt, true
		}
	}
	if resetAfterRaw.Valid && observedAtRaw.Valid {
		resetAfter, err := strconv.ParseInt(resetAfterRaw.String, 10, 64)
		if err == nil && resetAfter >= 0 {
			if observedAt, parseErr := time.Parse(time.RFC3339Nano, observedAtRaw.String); parseErr == nil {
				return observedAt.Add(time.Duration(resetAfter) * time.Second), true
			}
		}
	}
	return time.Time{}, false
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

// ListDueResets 返回所有已到期窗口；baseline-only 与 5h donation-only 窗口也必须清理。
func (r *userAccountWindowQuotaRepository) ListDueResets(ctx context.Context, now time.Time) ([]service.AccountWindowReset, error) {
	client := clientFromContext(ctx, r.client)
	const q = `SELECT DISTINCT q.account_id, q.window_type
		FROM user_account_window_quotas q
		JOIN accounts a ON a.id = q.account_id
		WHERE q.deleted_at IS NULL AND a.deleted_at IS NULL
		  AND COALESCE(a.extra->>'window_quota_shared', 'false') = 'true'
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

// SyncAccountMembers 只同步成员集合。保留成员的手工 limit、已归因、reset 和 donation；
// 新成员仅补缺失窗口行，显式 /equalize 才会批量覆盖 limit_percent。
func (r *userAccountWindowQuotaRepository) SyncAccountMembers(ctx context.Context, accountID int64, userIDs []int64, ceiling5h, ceiling7d float64) ([]service.AccountWindowEqualizationResult, error) {
	var results []service.AccountWindowEqualizationResult
	err := r.withWindowQuotaTx(ctx, func(txCtx context.Context, client *dbent.Client) error {
		rows, err := client.QueryContext(txCtx, `SELECT COALESCE(extra, '{}'::jsonb)::text
			FROM accounts
			WHERE id = $1 AND deleted_at IS NULL
			  AND COALESCE(extra->>'window_quota_shared', 'false') = 'true'
			FOR UPDATE`, accountID)
		if err != nil {
			return fmt.Errorf("lock account %d before syncing members: %w", accountID, err)
		}
		var extraRaw string
		if rows.Next() {
			err = rows.Scan(&extraRaw)
		} else if rows.Err() != nil {
			err = rows.Err()
		} else {
			err = service.ErrAccountWindowInvalidMembers
		}
		_ = rows.Close()
		if err != nil {
			return fmt.Errorf("scan account %d before syncing members: %w", accountID, err)
		}
		syncNow := time.Now()
		effectiveCheckpoints := effectiveAccountWindowRecomputeCheckpoints([]byte(extraRaw), syncNow)
		checkpoints := durableAccountWindowAttributionCheckpoints([]byte(extraRaw), syncNow)

		selected := make(map[int64]struct{}, len(userIDs))
		placeholders := make([]string, len(userIDs))
		userArgs := make([]any, len(userIDs))
		for i, userID := range userIDs {
			selected[userID] = struct{}{}
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

		type currentQuota struct {
			userID     int64
			window     string
			limit      float64
			attributed float64
			resetAt    *time.Time
		}
		currentByWindow := map[string]map[int64]currentQuota{
			service.WindowType5h: {},
			service.WindowType7d: {},
		}
		quotaRows, err := client.QueryContext(txCtx, `SELECT user_id, window_type, limit_percent, attributed_percent, window_reset_at
			FROM user_account_window_quotas
			WHERE account_id = $1 AND deleted_at IS NULL
			ORDER BY window_type, user_id
			FOR UPDATE`, accountID)
		if err != nil {
			return fmt.Errorf("lock account %d members before sync: %w", accountID, err)
		}
		for quotaRows.Next() {
			var quota currentQuota
			var resetAt sql.NullTime
			if err := quotaRows.Scan(&quota.userID, &quota.window, &quota.limit, &quota.attributed, &resetAt); err != nil {
				_ = quotaRows.Close()
				return err
			}
			if resetAt.Valid {
				value := resetAt.Time
				quota.resetAt = &value
			}
			if currentByWindow[quota.window] == nil {
				currentByWindow[quota.window] = make(map[int64]currentQuota)
			}
			currentByWindow[quota.window][quota.userID] = quota
		}
		if err := quotaRows.Err(); err != nil {
			_ = quotaRows.Close()
			return err
		}
		_ = quotaRows.Close()

		for _, window := range []string{service.WindowType5h, service.WindowType7d} {
			checkpoint, ok := checkpoints[window]
			if !ok {
				continue
			}
			changed := false
			if checkpoint.PendingBasisByUser == nil {
				checkpoint.PendingBasisByUser = make(map[int64]float64)
			}

			// Freeze the membership boundary before adding new rows. Any usage that became
			// visible since the durable cursor is credited only to retained members; costs
			// from newly selected, removed, or otherwise inactive users stay unattributed.
			upper, hasUpper, err := latestUsageCursorInTx(txCtx, client, accountID, checkpoint.WindowStart, checkpoint.ResetAt, syncNow)
			if err != nil {
				return err
			}
			seen := attributionCursor{createdAt: checkpoint.SeenThroughCreatedAt, id: checkpoint.SeenThroughUsageLogID}
			if hasUpper && cursorAfter(upper, seen) {
				costByUser, err := readIncrementalUsageCostsInTx(txCtx, client, accountID, checkpoint.WindowStart, checkpoint.ResetAt, seen, upper)
				if err != nil {
					return err
				}
				for userID, cost := range costByUser {
					if cost <= 0 || math.IsNaN(cost) || math.IsInf(cost, 0) {
						continue
					}
					_, keep := selected[userID]
					_, wasMember := currentByWindow[window][userID]
					if keep && wasMember {
						checkpoint.PendingBasisByUser[userID] += cost
					} else {
						checkpoint.PendingUnattributedBasisUSD += cost
					}
				}
				checkpoint.SeenThroughCreatedAt = upper.createdAt
				checkpoint.SeenThroughUsageLogID = upper.id
				changed = true
			}

			for userID, quota := range currentByWindow[window] {
				if _, keep := selected[userID]; keep {
					continue
				}
				if quota.attributed > 0 {
					checkpoint.UnattributedPercent = roundWindowPercent(checkpoint.UnattributedPercent + quota.attributed)
					changed = true
				}
				if cost := checkpoint.PendingBasisByUser[userID]; cost > 0 {
					checkpoint.PendingUnattributedBasisUSD += cost
					delete(checkpoint.PendingBasisByUser, userID)
					changed = true
				}
			}
			if changed {
				if err := persistAccountWindowCheckpointInTx(txCtx, client, accountID, service.AccountWindowAttributionCheckpointExtraKey(window), checkpoint); err != nil {
					return err
				}
				checkpoints[window] = checkpoint
				effectiveCheckpoints[window] = checkpoint
			}
		}

		removeArgs := make([]any, 0, len(userIDs)+2)
		removeArgs = append(removeArgs, accountID, syncNow)
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
			current := currentByWindow[item.window]
			retainedLimit := 0.0
			newUsers := make([]int64, 0)
			var resetAt *time.Time
			if checkpoint, ok := effectiveCheckpoints[item.window]; ok && checkpoint.ResetAt != nil && checkpoint.ResetAt.After(syncNow) {
				value := *checkpoint.ResetAt
				resetAt = &value
			}
			for _, userID := range userIDs {
				if quota, ok := current[userID]; ok {
					retainedLimit += quota.limit
					if resetAt == nil && quota.resetAt != nil && quota.resetAt.After(syncNow) {
						value := *quota.resetAt
						resetAt = &value
					}
					continue
				}
				newUsers = append(newUsers, userID)
			}
			defaultLimit := 0.0
			if len(newUsers) > 0 {
				available := math.Max(0, item.ceiling-retainedLimit)
				perSelection := item.ceiling / float64(len(userIDs))
				defaultLimit = math.Floor(math.Min(perSelection, available/float64(len(newUsers)))*10000) / 10000
			}
			const insertQ = `INSERT INTO user_account_window_quotas
				(user_id, account_id, window_type, limit_percent, attributed_percent, donate_pool_fraction, window_reset_at, created_at, updated_at)
				VALUES ($1, $2, $3, $4, 0, 0, $5, $6, $6)
				ON CONFLICT (user_id, account_id, window_type) WHERE deleted_at IS NULL DO NOTHING`
			for _, userID := range newUsers {
				if _, err := client.ExecContext(txCtx, insertQ, userID, accountID, item.window, defaultLimit, nullableTime(resetAt), syncNow); err != nil {
					return fmt.Errorf("add account %d member %d window %s: %w", accountID, userID, item.window, err)
				}
			}
			results = append(results, service.AccountWindowEqualizationResult{
				WindowType:        item.window,
				ActiveMemberCount: len(userIDs),
				SharePercent:      defaultLimit,
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

func windowLengthSecondsForRepository(window string) int {
	if window == service.WindowType7d {
		return 7 * 24 * 60 * 60
	}
	return 5 * 60 * 60
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
