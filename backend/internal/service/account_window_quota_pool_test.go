package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func awqRec(userID int64, window string, limit, used, frac float64) UserAccountWindowQuotaRecord {
	return UserAccountWindowQuotaRecord{
		UserID:             userID,
		AccountID:          1,
		WindowType:         window,
		LimitPercent:       limit,
		AttributedPercent:  used,
		DonatePoolFraction: frac,
	}
}

func awqApproxEq(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-6
}

// 用户示例：base=100、已用20、捐出自己份额的 50% → 自留有效上限 50（已用20 + 自留30），捐出 50。
func TestAccountWindowPool_PartialDonationKeepAndPool(t *testing.T) {
	pv := buildAccountWindowPoolView([]UserAccountWindowQuotaRecord{
		awqRec(2, WindowType5h, 100, 20, 0.5),
	})
	if got := pv.donor5hKeepCap(2); !awqApproxEq(got, 50) {
		t.Fatalf("keepCap = %v, want 50", got)
	}
	if got := pv.pool5hAvailable(); !awqApproxEq(got, 50) {
		t.Fatalf("pool = %v, want 50", got)
	}
}

// 已用(60) 超过自留线(50) → 冻结在已用值，仅捐真正未用的 40（捐赠者自保护）。
func TestAccountWindowPool_DonorUsedExceedsKeep(t *testing.T) {
	pv := buildAccountWindowPoolView([]UserAccountWindowQuotaRecord{
		awqRec(2, WindowType5h, 100, 60, 0.5),
	})
	if got := pv.donor5hKeepCap(2); !awqApproxEq(got, 60) {
		t.Fatalf("keepCap = %v, want 60", got)
	}
	if got := pv.pool5hAvailable(); !awqApproxEq(got, 40) {
		t.Fatalf("pool = %v, want 40", got)
	}
}

// Bob 全捐(pool=23)，仅 Alice 顶满 → Alice 有效上限 = 23 + 23 = 46；且 Σ有效上限 == Σbase（守恒）。
func TestAccountWindowPool_NeedyBorrowAndConservation(t *testing.T) {
	recs := []UserAccountWindowQuotaRecord{
		awqRec(1, WindowType5h, 23, 23, 0), // Alice 顶满
		awqRec(2, WindowType5h, 23, 0, 1),  // Bob 全捐
		awqRec(3, WindowType5h, 23, 10, 0), // Carol 未顶
		awqRec(4, WindowType5h, 23, 10, 0), // Dave 未顶
	}
	pv := buildAccountWindowPoolView(recs)
	if got := pv.needy5hCount(); got != 1 {
		t.Fatalf("needy = %d, want 1", got)
	}
	if got := pv.effective5hLimit(1); !awqApproxEq(got, 46) {
		t.Fatalf("alice effective = %v, want 46", got)
	}
	sumEff := pv.effective5hLimit(1) + pv.effective5hLimit(2) + pv.effective5hLimit(3) + pv.effective5hLimit(4)
	if !awqApproxEq(sumEff, 92) {
		t.Fatalf("sum effective = %v, want 92 (conserved)", sumEff)
	}
}

// 两人同时顶满 → 救急池(23)均分，各 +11.5 → 有效上限 34.5。
func TestAccountWindowPool_EqualSplitAmongNeedy(t *testing.T) {
	recs := []UserAccountWindowQuotaRecord{
		awqRec(1, WindowType5h, 23, 23, 0),
		awqRec(3, WindowType5h, 23, 23, 0),
		awqRec(2, WindowType5h, 23, 0, 1), // pool 23
	}
	pv := buildAccountWindowPoolView(recs)
	if got := pv.needy5hCount(); got != 2 {
		t.Fatalf("needy = %d, want 2", got)
	}
	if got := pv.effective5hLimit(1); !awqApproxEq(got, 34.5) {
		t.Fatalf("effective = %v, want 34.5", got)
	}
}

// 池已被借空：后来的缺额者不能再借到别人已经消费掉的容量（超借防护）。
func TestAccountWindowPool_ExhaustedPoolBlocksLateBorrower(t *testing.T) {
	recs := []UserAccountWindowQuotaRecord{
		awqRec(1, WindowType5h, 23, 46, 0), // Alice 已把池 23 全部借走
		awqRec(2, WindowType5h, 23, 0, 1),  // Bob 全捐 23
		awqRec(3, WindowType5h, 23, 23, 0), // Carol 刚顶满，想借
	}
	pv := buildAccountWindowPoolView(recs)
	if got := pv.pool5hAvailable(); !awqApproxEq(got, 0) {
		t.Fatalf("pool available = %v, want 0", got)
	}
	if got := pv.effective5hLimit(3); !awqApproxEq(got, 23) {
		t.Fatalf("carol effective = %v, want 23 (pool exhausted)", got)
	}
	// Alice 的既有借用不追回，但公平份额缩到 11.5 → 已超出即拦截，不能继续借。
	if got := pv.effective5hLimit(1); !awqApproxEq(got, 34.5) {
		t.Fatalf("alice effective = %v, want 34.5", got)
	}
}

// 正当借用 7d 救急池的用户不应被排除在 5h 借用之外（与 7d 判定口径一致）。
func TestAccountWindowPool_7dPoolBorrowerCanStillBorrow5h(t *testing.T) {
	recs := []UserAccountWindowQuotaRecord{
		awqRec(1, WindowType5h, 23, 23, 0), // Alice 5h 顶满
		awqRec(1, WindowType7d, 23, 23, 0), // Alice 7d 顶满自己份额
		awqRec(2, WindowType5h, 23, 0, 1),  // Bob 捐 5h
		awqRec(2, WindowType7d, 23, 0, 1),  // Bob 捐 7d → Alice 7d 仍有余量
	}
	pv := buildAccountWindowPoolView(recs)
	if !pv.within7d(1) {
		t.Fatalf("alice should be within 7d via the 7d pool")
	}
	if got := pv.needy5hCount(); got != 1 {
		t.Fatalf("needy = %d, want 1", got)
	}
	if got := pv.effective5hLimit(1); !awqApproxEq(got, 46) {
		t.Fatalf("alice 5h effective = %v, want 46", got)
	}
}

func TestAccountWindowPool_SubtractsBorrowedUsageFromAvailable(t *testing.T) {
	records := []UserAccountWindowQuotaRecord{
		awqRec(1, WindowType5h, 23, 35, 0),
		awqRec(2, WindowType5h, 23, 0, 1),
	}
	pv := buildAccountWindowPoolView(records)
	if got := pv.pool5hAvailable(); !awqApproxEq(got, 11) {
		t.Fatalf("available pool = %v, want 11 after 12 was borrowed", got)
	}
	if got := MinimumAccountWindowDonateFraction(records, 2, WindowType5h); !awqApproxEq(got, 12.0/23.0) {
		t.Fatalf("minimum donation fraction = %v, want %v", got, 12.0/23.0)
	}
}

// Bob 捐出 23%，Alice 已经借用其中 12%；Bob 最多只能收回未被借走的 11%，捐赠比例不得低于 12/23。
func TestSetDonateFraction_BlocksReclaimOfBorrowedQuota(t *testing.T) {
	repo := &stubWindowRepo{rows: []UserAccountWindowQuotaRecord{
		awqRec(1, WindowType5h, 23, 35, 0), // Alice 已使用自己 23% + 借用 12%
		awqRec(2, WindowType5h, 23, 0, 1),  // Bob 原先全捐 23%
	}}
	svc, _ := newQuotaServiceWithMiniRedis(t, repo)

	err := svc.SetDonateFraction(context.Background(), 2, 1, WindowType5h, 0)
	if !errors.Is(err, ErrAccountWindowDonationInUse) {
		t.Fatalf("error = %v, want ErrAccountWindowDonationInUse", err)
	}
	if len(repo.donateCalls) != 0 {
		t.Fatalf("donation write must be rejected, got %+v", repo.donateCalls)
	}
}

// 已借 12/23 时，Bob 可以把全捐降到恰好覆盖已借部分，但不能继续降低。
func TestSetDonateFraction_AllowsReclaimOnlyUnusedDonation(t *testing.T) {
	repo := &stubWindowRepo{rows: []UserAccountWindowQuotaRecord{
		awqRec(1, WindowType5h, 23, 35, 0),
		awqRec(2, WindowType5h, 23, 0, 1),
	}}
	svc, _ := newQuotaServiceWithMiniRedis(t, repo)
	minimum := 12.0 / 23.0

	if err := svc.SetDonateFraction(context.Background(), 2, 1, WindowType5h, minimum); err != nil {
		t.Fatalf("SetDonateFraction() error = %v", err)
	}
	if len(repo.donateCalls) != 1 || !awqApproxEq(repo.donateCalls[0].DonatePoolFraction, minimum) {
		t.Fatalf("donateCalls = %+v, want fraction %.6f", repo.donateCalls, minimum)
	}
}

// 7d 打满的用户不计入 needy、也借不到救急池。
func TestAccountWindowPool_SevenDayBlocksBorrow(t *testing.T) {
	recs := []UserAccountWindowQuotaRecord{
		awqRec(1, WindowType5h, 23, 23, 0),
		awqRec(1, WindowType7d, 23, 23, 0), // Alice 7d 打满
		awqRec(2, WindowType5h, 23, 0, 1),  // pool 23
	}
	pv := buildAccountWindowPoolView(recs)
	if got := pv.needy5hCount(); got != 0 {
		t.Fatalf("needy = %d, want 0 (alice 7d maxed)", got)
	}
	if got := pv.effective5hLimit(1); !awqApproxEq(got, 23) {
		t.Fatalf("effective = %v, want 23 (no borrow)", got)
	}
}

type windowCostRecomputeCall struct {
	accountID       int64
	window          string
	officialPercent float64
	windowStart     time.Time
	resetAt         *time.Time
}

// stubWindowRepo 是 UserAccountWindowQuotaRepository 的最小桩，仅 ListByAccount/ListByUser 返回固定数据。
type stubWindowRepo struct {
	rows             []UserAccountWindowQuotaRecord
	adminRows        []AdminWindowQuotaOverviewRow
	setCalls         []UserAccountWindowQuotaRecord
	donateCalls      []UserAccountWindowQuotaRecord
	costCalls        []windowCostRecomputeCall
	costRecomputeErr error
	recomputeHook    func()
	maxConfigured    float64
	maxConfiguredErr error
	equalizeResults  []AccountWindowEqualizationResult
	equalizeErr      error
	equalizeAccount  int64
	equalize5h       float64
	equalize7d       float64
	sharedAccountIDs []int64
	peerMemberIDs    []int64
	syncUserIDs      []int64
	official5h       *float64
	official7d       *float64
	checkpoints      map[string]AccountWindowAttributionCheckpoint
}

type conditionalResetWindowRepo struct {
	stubWindowRepo
	due        []AccountWindowReset
	reset      bool
	resetCalls int
}

func (r *conditionalResetWindowRepo) ListDueResets(context.Context, time.Time) ([]AccountWindowReset, error) {
	return r.due, nil
}

func (r *conditionalResetWindowRepo) ResetWindowForAccountIfDue(context.Context, int64, string, time.Time) (bool, error) {
	r.resetCalls++
	return r.reset, nil
}

type blockingWindowRepo struct {
	stubWindowRepo
	mu           sync.Mutex
	calls        []windowCostRecomputeCall
	firstEntered chan struct{}
	releaseFirst chan struct{}
	firstOnce    sync.Once
}

func (r *blockingWindowRepo) ApplyWindowSharesFromCheckpoint(_ context.Context, accountID int64, window string, candidate AccountWindowAttributionCheckpoint, resetAt *time.Time) (AccountWindowAttributionCheckpoint, bool, error) {
	officialPercent, windowStart := candidate.LatestOfficialPercent, candidate.WindowStart
	if officialPercent == 10 {
		r.firstOnce.Do(func() { close(r.firstEntered) })
		<-r.releaseFirst
	}
	var copiedResetAt *time.Time
	if resetAt != nil {
		value := *resetAt
		copiedResetAt = &value
	}
	r.mu.Lock()
	r.calls = append(r.calls, windowCostRecomputeCall{
		accountID: accountID, window: window, officialPercent: officialPercent,
		windowStart: windowStart, resetAt: copiedResetAt,
	})
	r.mu.Unlock()
	return candidate, true, nil
}

func (s *stubWindowRepo) ResetWindowForAccountIfDue(context.Context, int64, string, time.Time) (bool, error) {
	return true, nil
}
func (s *stubWindowRepo) GetByUserAccountWindow(context.Context, int64, int64, string) (*UserAccountWindowQuotaRecord, error) {
	return nil, nil
}
func (s *stubWindowRepo) ListByUser(context.Context, int64) ([]UserAccountWindowQuotaRecord, error) {
	return s.rows, nil
}
func (s *stubWindowRepo) ListByAccount(context.Context, int64) ([]UserAccountWindowQuotaRecord, error) {
	return s.rows, nil
}
func (s *stubWindowRepo) ListDueResets(context.Context, time.Time) ([]AccountWindowReset, error) {
	return nil, nil
}
func (s *stubWindowRepo) SetLimitForUserAccount(_ context.Context, userID, accountID int64, window string, limit float64) error {
	s.setCalls = append(s.setCalls, UserAccountWindowQuotaRecord{
		UserID:       userID,
		AccountID:    accountID,
		WindowType:   window,
		LimitPercent: limit,
	})
	return nil
}
func (s *stubWindowRepo) ApplyWindowSharesFromCheckpoint(_ context.Context, accountID int64, window string, candidate AccountWindowAttributionCheckpoint, resetAt *time.Time) (AccountWindowAttributionCheckpoint, bool, error) {
	candidate.ResetAt = resetAt
	if s.checkpoints == nil {
		s.checkpoints = make(map[string]AccountWindowAttributionCheckpoint)
	}
	key := fmt.Sprintf("%d:%s", accountID, window)
	var existing *AccountWindowAttributionCheckpoint
	if stored, ok := s.checkpoints[key]; ok {
		copy := stored
		existing = &copy
	}
	winner, accepted := SelectAccountWindowAttributionCheckpoint(existing, candidate, resetAt != nil)
	if !accepted {
		return winner, false, nil
	}
	if s.recomputeHook != nil {
		s.recomputeHook()
	}
	var copiedResetAt *time.Time
	if winner.ResetAt != nil {
		value := *winner.ResetAt
		copiedResetAt = &value
	}
	s.costCalls = append(s.costCalls, windowCostRecomputeCall{
		accountID: accountID, window: window, officialPercent: winner.LatestOfficialPercent,
		windowStart: winner.WindowStart, resetAt: copiedResetAt,
	})
	if s.costRecomputeErr != nil {
		return AccountWindowAttributionCheckpoint{}, false, s.costRecomputeErr
	}
	s.checkpoints[key] = winner
	return winner, true, nil
}

func (s *stubWindowRepo) ListAllWithUser(context.Context) ([]AdminWindowQuotaOverviewRow, error) {
	return s.adminRows, nil
}
func (s *stubWindowRepo) SetDonatePoolFraction(_ context.Context, userID, accountID int64, window string, fraction, _ float64) error {
	s.donateCalls = append(s.donateCalls, UserAccountWindowQuotaRecord{
		UserID:             userID,
		AccountID:          accountID,
		WindowType:         window,
		DonatePoolFraction: fraction,
	})
	return nil
}
func (s *stubWindowRepo) GetMaxConfiguredSumForWindow(context.Context, string) (float64, error) {
	return s.maxConfigured, s.maxConfiguredErr
}
func (s *stubWindowRepo) EqualizeActiveMemberLimits(_ context.Context, accountID int64, ceiling5h, ceiling7d float64) ([]AccountWindowEqualizationResult, error) {
	s.equalizeAccount = accountID
	s.equalize5h = ceiling5h
	s.equalize7d = ceiling7d
	return s.equalizeResults, s.equalizeErr
}
func (s *stubWindowRepo) SyncAccountMembers(_ context.Context, accountID int64, userIDs []int64, ceiling5h, ceiling7d float64) ([]AccountWindowEqualizationResult, error) {
	s.equalizeAccount = accountID
	s.equalize5h = ceiling5h
	s.equalize7d = ceiling7d
	s.syncUserIDs = append([]int64(nil), userIDs...)
	for _, window := range accountWindowTypes {
		checkpoint, ok := s.checkpoints[fmt.Sprintf("%d:%s", accountID, window)]
		if !ok || !validAccountWindowCostCheckpoint(&checkpoint) {
			continue
		}
		resetAt := checkpoint.ResetAt
		if resetAt == nil {
			fallback := checkpoint.WindowStart.Add(time.Duration(windowLengthSeconds(window)) * time.Second)
			resetAt = &fallback
		}
		if resetAt.After(time.Now()) {
			s.costCalls = append(s.costCalls, windowCostRecomputeCall{
				accountID: accountID, window: window, officialPercent: checkpoint.LatestOfficialPercent,
				windowStart: checkpoint.WindowStart, resetAt: resetAt,
			})
		}
	}
	return s.equalizeResults, s.equalizeErr
}
func (s *stubWindowRepo) ListSharedAccountIDs(context.Context) ([]int64, error) {
	return append([]int64(nil), s.sharedAccountIDs...), nil
}
func (s *stubWindowRepo) ListPeerSharedAccountMemberUserIDs(context.Context, int64) ([]int64, error) {
	return append([]int64(nil), s.peerMemberIDs...), nil
}
func (s *stubWindowRepo) GetAccountOfficialWindowPercent(_ context.Context, _ int64, window string) (float64, bool, error) {
	value := s.official5h
	if window == WindowType7d {
		value = s.official7d
	}
	if value == nil {
		return 0, false, nil
	}
	return *value, true, nil
}

func newStubQuotaService(t *testing.T, rows []UserAccountWindowQuotaRecord) *AccountWindowQuotaService {
	t.Helper()
	svc, _ := newQuotaServiceWithMiniRedis(t, &stubWindowRepo{rows: rows})
	return svc
}

func TestListSharedAccountIDs_IncludesAccountsWithoutQuotaRows(t *testing.T) {
	repo := &stubWindowRepo{sharedAccountIDs: []int64{29, 31, 33}}
	svc, _ := newQuotaServiceWithMiniRedis(t, repo)
	ids, err := svc.ListSharedAccountIDs(context.Background())
	if err != nil {
		t.Fatalf("ListSharedAccountIDs() error = %v", err)
	}
	if len(ids) != 3 || ids[0] != 29 || ids[1] != 31 || ids[2] != 33 {
		t.Fatalf("ids = %#v, want [29 31 33]", ids)
	}
}

func TestBootstrapSharedAccountMembers_InheritsPeerMembersForEmptyAccount(t *testing.T) {
	repo := &stubWindowRepo{peerMemberIDs: []int64{33, 1, 32, 31}}
	svc, _ := newQuotaServiceWithMiniRedis(t, repo)
	if _, err := svc.BootstrapSharedAccountMembers(context.Background(), 34); err != nil {
		t.Fatalf("BootstrapSharedAccountMembers() error = %v", err)
	}
	want := []int64{1, 31, 32, 33}
	if len(repo.syncUserIDs) != len(want) {
		t.Fatalf("sync user ids = %#v, want %#v", repo.syncUserIDs, want)
	}
	for i := range want {
		if repo.syncUserIDs[i] != want[i] {
			t.Fatalf("sync user ids = %#v, want %#v", repo.syncUserIDs, want)
		}
	}
}

func TestBootstrapSharedAccountMembers_DoesNotOverwriteExistingMembers(t *testing.T) {
	repo := &stubWindowRepo{
		rows:          []UserAccountWindowQuotaRecord{awqRec(1, WindowType5h, 23, 0, 0)},
		peerMemberIDs: []int64{1, 31, 32, 33},
	}
	svc, _ := newQuotaServiceWithMiniRedis(t, repo)
	if _, err := svc.BootstrapSharedAccountMembers(context.Background(), 29); err != nil {
		t.Fatalf("BootstrapSharedAccountMembers() error = %v", err)
	}
	if len(repo.syncUserIDs) != 0 {
		t.Fatalf("existing members must not be overwritten, got sync ids %#v", repo.syncUserIDs)
	}
}

func TestSetAccountMembers_RecomputesFromOfficialCheckpointUsingDollarPath(t *testing.T) {
	repo := &stubWindowRepo{}
	svc, mr := newQuotaServiceWithMiniRedis(t, repo)
	now := time.Now().UTC().Truncate(time.Second)
	windowStart := now.Add(-time.Hour)
	checkpoint := newAccountWindowCostCheckpoint(31.5, windowStart, now)
	repo.checkpoints = map[string]AccountWindowAttributionCheckpoint{
		fmt.Sprintf("%d:%s", int64(29), WindowType5h): checkpoint,
	}
	encoded, err := json.Marshal(checkpoint)
	require.NoError(t, err)
	mr.Set(accountWindowCheckpointKey(29, WindowType5h), string(encoded))

	_, err = svc.SetAccountMembers(context.Background(), 29, []int64{31, 1})
	require.NoError(t, err)
	require.Equal(t, []int64{1, 31}, repo.syncUserIDs)
	require.Len(t, repo.costCalls, 1)
	require.Equal(t, int64(29), repo.costCalls[0].accountID)
	require.Equal(t, WindowType5h, repo.costCalls[0].window)
	require.InDelta(t, 31.5, repo.costCalls[0].officialPercent, 1e-9)
	require.WithinDuration(t, windowStart, repo.costCalls[0].windowStart, time.Second)
}

func TestAttributeWindow_KeepsOfficialPercentMonotonicAcrossSnapshotJitter(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	repo := &stubWindowRepo{}
	svc := NewAccountWindowQuotaService(repo, rdb)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	used := 27.0
	require.NoError(t, svc.attributeWindow(ctx, 29, WindowType5h, &used, nil, now))
	used = 20
	require.NoError(t, svc.attributeWindow(ctx, 29, WindowType5h, &used, nil, now.Add(time.Minute)))
	used = 27
	require.NoError(t, svc.attributeWindow(ctx, 29, WindowType5h, &used, nil, now.Add(2*time.Minute)))

	require.Len(t, repo.costCalls, 3)
	windowStart := repo.costCalls[0].windowStart
	for _, call := range repo.costCalls {
		require.WithinDuration(t, windowStart, call.windowStart, time.Second)
		require.InDelta(t, 27, call.officialPercent, 1e-9)
	}
}

func TestSelectAccountWindowAttributionCheckpoint_SevenDayBoundaryJitterStaysInSameWindow(t *testing.T) {
	windowStart := time.Date(2026, time.August, 8, 20, 31, 52, 0, time.UTC)
	resetAt := windowStart.Add(7 * 24 * time.Hour)
	observedAt := time.Date(2026, time.August, 9, 13, 0, 0, 0, time.UTC)
	existing := NewAccountWindowAttributionCheckpoint(26, windowStart, observedAt, &resetAt)

	jitteredResetAt := resetAt.Add(8*time.Minute + 30*time.Second)
	jitteredWindowStart := jitteredResetAt.Add(-7 * 24 * time.Hour)
	candidate := NewAccountWindowAttributionCheckpoint(29, jitteredWindowStart, observedAt.Add(time.Hour), &jitteredResetAt)

	winner, accepted := SelectAccountWindowAttributionCheckpoint(&existing, candidate, true)
	require.True(t, accepted)
	require.InDelta(t, 29, winner.LatestOfficialPercent, 1e-9)
	require.WithinDuration(t, windowStart, winner.WindowStart, time.Second)
	require.NotNil(t, winner.ResetAt)
	require.WithinDuration(t, resetAt, *winner.ResetAt, time.Second)
}

func TestAttributeWindow_LateOlderHigherSnapshotCannotOverwriteNewerObservation(t *testing.T) {
	repo := &stubWindowRepo{}
	svc, mr := newQuotaServiceWithMiniRedis(t, repo)
	ctx := context.Background()
	oldObservedAt := time.Date(2026, time.August, 2, 10, 0, 0, 0, time.UTC)
	newObservedAt := oldObservedAt.Add(5 * time.Minute)
	oldResetAfter := 60 * 60
	newResetAfter := 55 * 60
	lateOldResetAfter := 59 * 60

	oldUsed := 10.0
	require.NoError(t, svc.attributeWindow(ctx, 29, WindowType5h, &oldUsed, &oldResetAfter, oldObservedAt))
	newUsed := 20.0
	require.NoError(t, svc.attributeWindow(ctx, 29, WindowType5h, &newUsed, &newResetAfter, newObservedAt))
	lateOlderHigh := 30.0
	require.NoError(t, svc.attributeWindow(ctx, 29, WindowType5h, &lateOlderHigh, &lateOldResetAfter, oldObservedAt.Add(time.Minute)))

	require.Len(t, repo.costCalls, 2)
	require.InDelta(t, 10, repo.costCalls[0].officialPercent, 1e-9)
	require.InDelta(t, 20, repo.costCalls[1].officialPercent, 1e-9)
	stored, err := mr.Get(accountWindowCheckpointKey(29, WindowType5h))
	require.NoError(t, err)
	var checkpoint accountWindowAttributionCheckpoint
	require.NoError(t, json.Unmarshal([]byte(stored), &checkpoint))
	require.InDelta(t, 20, checkpoint.LatestOfficialPercent, 1e-9)
	require.WithinDuration(t, newObservedAt, checkpoint.LatestObservedAt, time.Second)
}

func TestAttributeWindow_SerializesCheckpointWinnerAndDatabaseRecompute(t *testing.T) {
	repo := &blockingWindowRepo{
		firstEntered: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	svc, mr := newQuotaServiceWithMiniRedis(t, repo)
	ctx := context.Background()
	oldObservedAt := time.Date(2026, time.August, 2, 10, 0, 0, 0, time.UTC)
	newObservedAt := oldObservedAt.Add(5 * time.Minute)
	oldResetAfter := 60 * 60
	newResetAfter := 55 * 60
	oldUsed := 10.0
	newUsed := 20.0

	oldDone := make(chan error, 1)
	go func() {
		oldDone <- svc.attributeWindow(ctx, 29, WindowType5h, &oldUsed, &oldResetAfter, oldObservedAt)
	}()
	select {
	case <-repo.firstEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("older snapshot did not enter database recompute")
	}

	newDone := make(chan error, 1)
	go func() {
		newDone <- svc.attributeWindow(ctx, 29, WindowType5h, &newUsed, &newResetAfter, newObservedAt)
	}()
	select {
	case err := <-newDone:
		t.Fatalf("newer snapshot bypassed account-window lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(repo.releaseFirst)
	require.NoError(t, <-oldDone)
	require.NoError(t, <-newDone)

	repo.mu.Lock()
	calls := append([]windowCostRecomputeCall(nil), repo.calls...)
	repo.mu.Unlock()
	require.Len(t, calls, 2)
	require.InDelta(t, 10, calls[0].officialPercent, 1e-9)
	require.InDelta(t, 20, calls[1].officialPercent, 1e-9)

	stored, err := mr.Get(accountWindowCheckpointKey(29, WindowType5h))
	require.NoError(t, err)
	var checkpoint accountWindowAttributionCheckpoint
	require.NoError(t, json.Unmarshal([]byte(stored), &checkpoint))
	require.InDelta(t, 20, checkpoint.LatestOfficialPercent, 1e-9)
	require.WithinDuration(t, newObservedAt, checkpoint.LatestObservedAt, time.Second)
}

func TestResetDueWindowsDeletesCheckpointOnlyWhenDatabaseWindowIsStillDue(t *testing.T) {
	tests := []struct {
		name           string
		reset          bool
		wantCheckpoint bool
	}{
		{name: "new official window superseded scan", reset: false, wantCheckpoint: true},
		{name: "persisted window remains due", reset: true, wantCheckpoint: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &conditionalResetWindowRepo{
				due:   []AccountWindowReset{{AccountID: 29, WindowType: WindowType5h}},
				reset: tt.reset,
			}
			svc, mr := newQuotaServiceWithMiniRedis(t, repo)
			key := accountWindowCheckpointKey(29, WindowType5h)
			mr.Set(key, `{"basis":"full_window_total_cost_v3"}`)

			svc.ResetDueWindows(context.Background())

			require.Equal(t, 1, repo.resetCalls)
			_, err := mr.Get(key)
			if tt.wantCheckpoint {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestCheckUserAccountEligible_BlocksUnassignedSharedMember(t *testing.T) {
	svc := newStubQuotaService(t, []UserAccountWindowQuotaRecord{
		awqRec(1, WindowType5h, 23, 6.5, 0),
		awqRec(1, WindowType7d, 23, 1, 0),
		awqRec(2, WindowType5h, 23, 6.5, 0),
		awqRec(2, WindowType7d, 23, 1, 0),
	})
	eligible, window, _ := svc.CheckUserAccountEligible(context.Background(), 99, 1)
	if eligible || window != WindowType5h {
		t.Fatalf("unassigned user eligible=%v window=%q, want blocked on 5h", eligible, window)
	}
}

// 顶满自己 5h 份额、但救急池有余 → 放行（借用救急池）。
func TestCheckUserAccountEligible_PoolAllowsBorrow(t *testing.T) {
	svc := newStubQuotaService(t, []UserAccountWindowQuotaRecord{
		awqRec(1, WindowType5h, 23, 23, 0),
		awqRec(2, WindowType5h, 23, 0, 1),
	})
	eligible, _, _ := svc.CheckUserAccountEligible(context.Background(), 1, 1)
	if !eligible {
		t.Fatalf("alice should be eligible via emergency pool")
	}
}

func TestCheckUserAccountEligible_ExpiredWindowDoesNotBlockBeforeResetWorker(t *testing.T) {
	past := time.Now().UTC().Add(-time.Minute)
	member := awqRec(1, WindowType5h, 23, 23, 0)
	member.WindowResetAt = &past
	other := awqRec(2, WindowType5h, 23, 10, 0)
	other.WindowResetAt = &past

	svc := newStubQuotaService(t, []UserAccountWindowQuotaRecord{member, other})
	eligible, window, resetAt := svc.CheckUserAccountEligible(context.Background(), 1, 1)
	require.True(t, eligible)
	require.Empty(t, window)
	require.Nil(t, resetAt)
}

// 7d 周配额打满 → 即使 5h 救急池有余也拦截（铁底线）。
func TestCheckUserAccountEligible_SevenDayHardBlock(t *testing.T) {
	svc := newStubQuotaService(t, []UserAccountWindowQuotaRecord{
		awqRec(1, WindowType5h, 23, 0, 0),
		awqRec(1, WindowType7d, 23, 23, 0),
		awqRec(2, WindowType5h, 23, 0, 1),
	})
	eligible, window, _ := svc.CheckUserAccountEligible(context.Background(), 1, 1)
	if eligible || window != WindowType7d {
		t.Fatalf("alice should be blocked on 7d, got eligible=%v window=%q", eligible, window)
	}
}

// 无人捐赠（池为空）→ 顶满自己份额即拦截（退化为原硬性上限行为）。
func TestCheckUserAccountEligible_NoPoolBlocksAtBase(t *testing.T) {
	svc := newStubQuotaService(t, []UserAccountWindowQuotaRecord{
		awqRec(1, WindowType5h, 23, 23, 0),
		awqRec(2, WindowType5h, 23, 10, 0),
	})
	eligible, window, _ := svc.CheckUserAccountEligible(context.Background(), 1, 1)
	if eligible || window != WindowType5h {
		t.Fatalf("alice should be blocked on 5h, got eligible=%v window=%q", eligible, window)
	}
}

// 自愿 7d 救急池：user2 自愿全捐 7d（pool=23），user1 7d 打满且非捐赠者 → 借到救急池放行。
func TestCheckEligible_7dVoluntaryDonationAllowsBorrow(t *testing.T) {
	svc := newStubQuotaService(t, []UserAccountWindowQuotaRecord{
		awqRec(1, WindowType5h, 23, 0, 0),
		awqRec(1, WindowType7d, 23, 23, 0), // user1 顶满自己 7d，未捐
		awqRec(2, WindowType7d, 23, 0, 1),  // user2 自愿全捐 7d → pool=23
	})
	eligible, _, _ := svc.CheckUserAccountEligible(context.Background(), 1, 1)
	if !eligible {
		t.Fatalf("user1 should borrow the voluntary 7d pool")
	}
}

// 无人捐 7d：打满即拦（纯铁底线，谁也动不了你没捐的份额）。
func TestCheckEligible_7dHardCapWhenNoDonation(t *testing.T) {
	svc := newStubQuotaService(t, []UserAccountWindowQuotaRecord{
		awqRec(1, WindowType5h, 23, 0, 0),
		awqRec(1, WindowType7d, 23, 23, 0),
		awqRec(2, WindowType7d, 23, 0, 0), // 没人捐 → pool=0
	})
	eligible, window, _ := svc.CheckUserAccountEligible(context.Background(), 1, 1)
	if eligible || window != WindowType7d {
		t.Fatalf("user1 should be hard-blocked on 7d when nobody donated, got eligible=%v window=%q", eligible, window)
	}
}

// 7d 捐赠者自留保护：捐 50%、已用 10 → 自留有效上限 max(10, 23×0.5)=11.5，pool=23−11.5=11.5。
func TestAccountWindowPool_7dDonorKeepAndPool(t *testing.T) {
	pv := buildAccountWindowPoolView([]UserAccountWindowQuotaRecord{
		awqRec(2, WindowType7d, 23, 10, 0.5),
	})
	if got := pv.donor7dKeepCap(2); !awqApproxEq(got, 11.5) {
		t.Fatalf("7d keepCap = %v, want 11.5", got)
	}
	if got := pv.pool7dAvailable(); !awqApproxEq(got, 11.5) {
		t.Fatalf("7d pool = %v, want 11.5", got)
	}
}

// 账号级硬闸只认 OpenAI 官方窗口快照；成员归因之和不能充当账号总量。
func TestCheckEligible_AccountCeilingIgnoresAttributedMemberSum(t *testing.T) {
	official := 50.0
	svc, _ := newQuotaServiceWithMiniRedis(t, &stubWindowRepo{
		rows: []UserAccountWindowQuotaRecord{
			awqRec(1, WindowType5h, 50, 10, 0),
			awqRec(2, WindowType5h, 50, 85, 0), // 成员之和 95，但官方仅 50
		},
		official5h: &official,
	})
	eligible, window, _ := svc.CheckUserAccountEligible(context.Background(), 1, 1)
	if !eligible || window != "" {
		t.Fatalf("member attribution sum must not hard-stop account, got eligible=%v window=%q", eligible, window)
	}
}

// 官方窗口达到 ceiling 时，即使当前成员远未到个人上限也必须全员拦截。
func TestCheckEligible_DoesNotUseRedisCheckpointAsOfficialTruth(t *testing.T) {
	svc, mr := newQuotaServiceWithMiniRedis(t, &stubWindowRepo{
		rows: []UserAccountWindowQuotaRecord{
			awqRec(1, WindowType5h, 50, 10, 0),
		},
	})
	now := time.Now()
	resetAt := now.Add(2 * time.Hour)
	checkpoint := NewAccountWindowAttributionCheckpoint(95, resetAt.Add(-5*time.Hour), now, &resetAt)
	encoded, err := json.Marshal(checkpoint)
	require.NoError(t, err)
	mr.Set(accountWindowCheckpointKey(1, WindowType5h), string(encoded))

	eligible, window, _ := svc.CheckUserAccountEligible(context.Background(), 1, 1)
	require.True(t, eligible)
	require.Empty(t, window)
}

func TestCheckEligible_AccountCeilingHardStopFromOfficialSnapshot(t *testing.T) {
	official := 95.0
	svc, _ := newQuotaServiceWithMiniRedis(t, &stubWindowRepo{
		rows: []UserAccountWindowQuotaRecord{
			awqRec(1, WindowType5h, 50, 10, 0),
			awqRec(2, WindowType5h, 50, 20, 0),
		},
		official5h: &official,
	})
	eligible, window, _ := svc.CheckUserAccountEligible(context.Background(), 1, 1)
	if eligible || window != WindowType5h {
		t.Fatalf("official snapshot should hard-stop at account ceiling, got eligible=%v window=%q", eligible, window)
	}
}

func newQuotaServiceWithMiniRedis(t *testing.T, repo UserAccountWindowQuotaRepository) (*AccountWindowQuotaService, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewAccountWindowQuotaService(repo, rdb), mr
}

func TestSyncOfficialSnapshot_RecomputesBothWindows(t *testing.T) {
	repo := &stubWindowRepo{rows: []UserAccountWindowQuotaRecord{
		{UserID: 1, AccountID: 29, WindowType: WindowType5h, AttributedPercent: 10},
		{UserID: 2, AccountID: 29, WindowType: WindowType5h, AttributedPercent: 10},
		{UserID: 1, AccountID: 29, WindowType: WindowType7d, AttributedPercent: 5},
		{UserID: 2, AccountID: 29, WindowType: WindowType7d, AttributedPercent: 5},
	}}
	svc, _ := newQuotaServiceWithMiniRedis(t, repo)
	used5h, reset5h, minutes5h := 42.0, 3600, 300
	used7d, reset7d, minutes7d := 18.0, 86400, 10080
	snapshot := &OpenAICodexUsageSnapshot{
		PrimaryUsedPercent:         &used5h,
		PrimaryResetAfterSeconds:   &reset5h,
		PrimaryWindowMinutes:       &minutes5h,
		SecondaryUsedPercent:       &used7d,
		SecondaryResetAfterSeconds: &reset7d,
		SecondaryWindowMinutes:     &minutes7d,
	}

	require.NoError(t, svc.SyncOfficialSnapshot(context.Background(), 29, snapshot))
	require.Len(t, repo.costCalls, 2)
	calls := map[string]windowCostRecomputeCall{}
	for _, call := range repo.costCalls {
		calls[call.window] = call
	}
	require.InDelta(t, 42.0, calls[WindowType5h].officialPercent, 1e-9)
	require.InDelta(t, 18.0, calls[WindowType7d].officialPercent, 1e-9)
	require.Equal(t, int64(29), calls[WindowType5h].accountID)
	require.Equal(t, int64(29), calls[WindowType7d].accountID)
}

func TestSyncOfficialSnapshot_PropagatesRecomputeError(t *testing.T) {
	repo := &stubWindowRepo{
		rows:             []UserAccountWindowQuotaRecord{{UserID: 1, AccountID: 29, WindowType: WindowType5h}},
		costRecomputeErr: errors.New("database unavailable"),
	}
	svc, _ := newQuotaServiceWithMiniRedis(t, repo)
	used5h, reset5h, minutes5h := 42.0, 3600, 300
	snapshot := &OpenAICodexUsageSnapshot{
		PrimaryUsedPercent:       &used5h,
		PrimaryResetAfterSeconds: &reset5h,
		PrimaryWindowMinutes:     &minutes5h,
	}

	err := svc.SyncOfficialSnapshot(context.Background(), 29, snapshot)
	require.ErrorContains(t, err, "sync 5h window")
	require.Len(t, repo.costCalls, 1)
}

func TestSyncOfficialSnapshot_EmptySnapshotIsNoOp(t *testing.T) {
	repo := &stubWindowRepo{rows: []UserAccountWindowQuotaRecord{{UserID: 1, AccountID: 29, WindowType: WindowType5h}}}
	svc, _ := newQuotaServiceWithMiniRedis(t, repo)

	require.NoError(t, svc.SyncOfficialSnapshot(context.Background(), 29, &OpenAICodexUsageSnapshot{}))
	require.Empty(t, repo.costCalls)
}

func TestSyncOfficialSnapshot_NewWindowDiscardsStaleSharesAndUsesWindowStart(t *testing.T) {
	oldReset := time.Now().Add(-7 * 24 * time.Hour)
	repo := &stubWindowRepo{rows: []UserAccountWindowQuotaRecord{
		{UserID: 1, AccountID: 29, WindowType: WindowType7d, AttributedPercent: 20.75, WindowResetAt: &oldReset},
		{UserID: 31, AccountID: 29, WindowType: WindowType7d, AttributedPercent: 20.75, WindowResetAt: &oldReset},
		{UserID: 32, AccountID: 29, WindowType: WindowType7d, AttributedPercent: 20.75, WindowResetAt: &oldReset},
		{UserID: 33, AccountID: 29, WindowType: WindowType7d, AttributedPercent: 20.75, WindowResetAt: &oldReset},
	}}
	svc, _ := newQuotaServiceWithMiniRedis(t, repo)
	used7d, reset7d, minutes7d := 4.0, 6*24*60*60, 10080
	snapshot := &OpenAICodexUsageSnapshot{
		SecondaryUsedPercent:       &used7d,
		SecondaryResetAfterSeconds: &reset7d,
		SecondaryWindowMinutes:     &minutes7d,
	}

	require.NoError(t, svc.SyncOfficialSnapshot(context.Background(), 29, snapshot))
	require.Len(t, repo.costCalls, 1)
	call := repo.costCalls[0]
	require.Equal(t, WindowType7d, call.window)
	require.NotNil(t, call.resetAt)
	require.WithinDuration(t, call.windowStart.Add(7*24*time.Hour), *call.resetAt, time.Second)
}

func TestSyncOfficialSnapshot_LegacyCheckpointRebuildsFromWindowStartWithCostBasis(t *testing.T) {
	now := time.Now().UTC()
	resetAt := now.Add(6 * 24 * time.Hour)
	repo := &stubWindowRepo{rows: []UserAccountWindowQuotaRecord{
		{UserID: 1, AccountID: 29, WindowType: WindowType7d, AttributedPercent: 20, WindowResetAt: &resetAt},
		{UserID: 31, AccountID: 29, WindowType: WindowType7d, AttributedPercent: 10, WindowResetAt: &resetAt},
	}}
	svc, mr := newQuotaServiceWithMiniRedis(t, repo)
	legacy := struct {
		At          time.Time         `json:"at"`
		WindowStart time.Time         `json:"window_start"`
		BaseShares  map[int64]float64 `json:"base_shares"`
	}{
		At:          now.Add(-10 * time.Minute),
		WindowStart: now.Add(-24 * time.Hour),
		BaseShares:  map[int64]float64{1: 20, 31: 10},
	}
	encoded, err := json.Marshal(legacy)
	require.NoError(t, err)
	mr.Set(accountWindowCheckpointKey(29, WindowType7d), string(encoded))

	used7d, reset7d, minutes7d := 42.0, 6*24*60*60, 10080
	snapshot := &OpenAICodexUsageSnapshot{
		SecondaryUsedPercent:       &used7d,
		SecondaryResetAfterSeconds: &reset7d,
		SecondaryWindowMinutes:     &minutes7d,
	}
	require.NoError(t, svc.SyncOfficialSnapshot(context.Background(), 29, snapshot))
	require.Len(t, repo.costCalls, 1)
	call := repo.costCalls[0]
	require.Equal(t, WindowType7d, call.window)
	require.NotNil(t, call.resetAt)
	require.WithinDuration(t, call.windowStart.Add(7*24*time.Hour), *call.resetAt, time.Second)

	stored, err := mr.Get(accountWindowCheckpointKey(29, WindowType7d))
	require.NoError(t, err)
	var checkpoint accountWindowAttributionCheckpoint
	require.NoError(t, json.Unmarshal([]byte(stored), &checkpoint))
	require.Equal(t, accountWindowAttributionBasis, checkpoint.Basis)
	require.Equal(t, accountWindowAttributionMode, checkpoint.AttributionMode)
	require.WithinDuration(t, checkpoint.WindowStart, checkpoint.At, time.Second)
	require.InDelta(t, 42, checkpoint.LatestOfficialPercent, 1e-9)
}

func TestAttributeWindow_NewWindowAllowsDropAndLateOldSnapshotCannotOverwrite(t *testing.T) {
	repo := &stubWindowRepo{}
	svc, mr := newQuotaServiceWithMiniRedis(t, repo)
	ctx := context.Background()
	oldObservedAt := time.Date(2026, time.July, 26, 4, 10, 0, 0, time.UTC)
	newObservedAt := time.Date(2026, time.August, 2, 4, 10, 0, 0, time.UTC)
	resetAfter := 6 * 24 * 60 * 60

	oldUsed := 76.0
	require.NoError(t, svc.attributeWindow(ctx, 29, WindowType7d, &oldUsed, &resetAfter, oldObservedAt))
	newUsed := 4.0
	require.NoError(t, svc.attributeWindow(ctx, 29, WindowType7d, &newUsed, &resetAfter, newObservedAt))
	lateOldUsed := 80.0
	require.NoError(t, svc.attributeWindow(ctx, 29, WindowType7d, &lateOldUsed, &resetAfter, oldObservedAt.Add(time.Minute)))

	require.Len(t, repo.costCalls, 2)
	require.InDelta(t, 76, repo.costCalls[0].officialPercent, 1e-9)
	require.InDelta(t, 4, repo.costCalls[1].officialPercent, 1e-9)
	require.True(t, repo.costCalls[1].windowStart.After(repo.costCalls[0].windowStart))

	stored, err := mr.Get(accountWindowCheckpointKey(29, WindowType7d))
	require.NoError(t, err)
	var checkpoint accountWindowAttributionCheckpoint
	require.NoError(t, json.Unmarshal([]byte(stored), &checkpoint))
	require.InDelta(t, 4, checkpoint.LatestOfficialPercent, 1e-9)
	require.WithinDuration(t, repo.costCalls[1].windowStart, checkpoint.WindowStart, time.Second)
}

func TestSetLimitForUserAccount_OverallocatedAllowsNonIncreasingChanges(t *testing.T) {
	for _, tc := range []struct {
		name      string
		newLimit  float64
		wantError bool
	}{
		{name: "decrease while still overallocated", newLimit: 50},
		{name: "hold while overallocated", newLimit: 60},
		{name: "increase while overallocated", newLimit: 61, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &stubWindowRepo{rows: []UserAccountWindowQuotaRecord{
				awqRec(1, WindowType5h, 60, 0, 0),
				awqRec(2, WindowType5h, 50, 0, 0),
			}}
			svc, _ := newQuotaServiceWithMiniRedis(t, repo)
			err := svc.SetLimitForUserAccount(context.Background(), 1, 1, WindowType5h, tc.newLimit)
			if tc.wantError {
				if !errors.Is(err, ErrAccountWindowCeilingExceeded) {
					t.Fatalf("error = %v, want ErrAccountWindowCeilingExceeded", err)
				}
				if len(repo.setCalls) != 0 {
					t.Fatalf("repository write should be rejected")
				}
				return
			}
			if err != nil {
				t.Fatalf("SetLimitForUserAccount() error = %v", err)
			}
			if len(repo.setCalls) != 1 || !awqApproxEq(repo.setCalls[0].LimitPercent, tc.newLimit) {
				t.Fatalf("setCalls = %+v, want one write with %.2f", repo.setCalls, tc.newLimit)
			}
		})
	}
}

func TestSetTotalCeiling_RejectsBelowExistingConfiguredSum(t *testing.T) {
	repo := &stubWindowRepo{maxConfigured: 95}
	svc, mr := newQuotaServiceWithMiniRedis(t, repo)

	err := svc.SetTotalCeiling(context.Background(), WindowType5h, 92)
	if !errors.Is(err, ErrAccountWindowCeilingBelowConfigured) {
		t.Fatalf("error = %v, want ErrAccountWindowCeilingBelowConfigured", err)
	}
	if mr.Exists(accountWindowCeilingKey(WindowType5h)) {
		t.Fatalf("ceiling must not be written when it is below existing configured sum")
	}
}

func TestListAllForAdmin_ReturnsPerAccountWindowSummaries(t *testing.T) {
	official5h := 12.5
	official7d := 34.5
	repo := &stubWindowRepo{
		adminRows: []AdminWindowQuotaOverviewRow{
			{UserID: 1, AccountID: 7, WindowType: WindowType5h, LimitPercent: 30, AttributedPercent: 10},
			{UserID: 2, AccountID: 7, WindowType: WindowType5h, LimitPercent: 30, AttributedPercent: 5},
			{UserID: 1, AccountID: 7, WindowType: WindowType7d, LimitPercent: 40, AttributedPercent: 20},
			{UserID: 2, AccountID: 7, WindowType: WindowType7d, LimitPercent: 30, AttributedPercent: 15},
		},
		official5h: &official5h,
		official7d: &official7d,
	}
	svc, mr := newQuotaServiceWithMiniRedis(t, repo)
	mr.Set(accountWindowCeilingKey(WindowType5h), "50")
	mr.Set(accountWindowCeilingKey(WindowType7d), "80")

	_, summaries, err := svc.ListAllForAdmin(context.Background())
	if err != nil {
		t.Fatalf("ListAllForAdmin() error = %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("summaries len = %d, want 2", len(summaries))
	}
	if got := summaries[0]; got.AccountID != 7 || got.WindowType != WindowType5h || got.MemberCount != 2 ||
		!awqApproxEq(got.ConfiguredSumPercent, 60) || got.OfficialUsedPercent == nil || !awqApproxEq(*got.OfficialUsedPercent, official5h) ||
		!awqApproxEq(got.CeilingPercent, 50) || !got.Overallocated {
		t.Fatalf("5h summary = %+v", got)
	}
	if got := summaries[1]; got.AccountID != 7 || got.WindowType != WindowType7d || got.MemberCount != 2 ||
		!awqApproxEq(got.ConfiguredSumPercent, 70) || got.OfficialUsedPercent == nil || !awqApproxEq(*got.OfficialUsedPercent, official7d) ||
		!awqApproxEq(got.CeilingPercent, 80) || got.Overallocated {
		t.Fatalf("7d summary = %+v", got)
	}
}

func TestListUserWindowsWithPool_UsesOnlyOfficialAccountUsage(t *testing.T) {
	official5h := 40.0
	repo := &stubWindowRepo{
		rows:       []UserAccountWindowQuotaRecord{awqRec(1, WindowType5h, 50, 10, 0)},
		official5h: &official5h,
	}
	svc, _ := newQuotaServiceWithMiniRedis(t, repo)

	views, err := svc.ListUserWindowsWithPool(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, views, 1)
	require.NotNil(t, views[0].AccountUsedPercent)
	require.InDelta(t, official5h, *views[0].AccountUsedPercent, 1e-9)
	require.NotEqual(t, views[0].AttributedPercent, *views[0].AccountUsedPercent, "成员归因不能冒充账号总量")
}

func TestListUserWindowsWithPool_OmitsAccountUsageWithoutOfficialSnapshot(t *testing.T) {
	repo := &stubWindowRepo{rows: []UserAccountWindowQuotaRecord{awqRec(1, WindowType5h, 50, 10, 0)}}
	svc, _ := newQuotaServiceWithMiniRedis(t, repo)

	views, err := svc.ListUserWindowsWithPool(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, views, 1)
	require.Nil(t, views[0].AccountUsedPercent)
}

func TestEqualizeAccountActiveMemberLimits_UsesCurrentCeilings(t *testing.T) {
	repo := &stubWindowRepo{equalizeResults: []AccountWindowEqualizationResult{
		{WindowType: WindowType5h, ActiveMemberCount: 2, SharePercent: 40, CeilingPercent: 80},
		{WindowType: WindowType7d, ActiveMemberCount: 4, SharePercent: 22.5, CeilingPercent: 90},
	}}
	svc, mr := newQuotaServiceWithMiniRedis(t, repo)
	mr.Set(accountWindowCeilingKey(WindowType5h), "80")
	mr.Set(accountWindowCeilingKey(WindowType7d), "90")

	results, err := svc.EqualizeAccountActiveMemberLimits(context.Background(), 7)
	if err != nil {
		t.Fatalf("EqualizeAccountActiveMemberLimits() error = %v", err)
	}
	if repo.equalizeAccount != 7 || !awqApproxEq(repo.equalize5h, 80) || !awqApproxEq(repo.equalize7d, 90) {
		t.Fatalf("repository args account=%d 5h=%.2f 7d=%.2f", repo.equalizeAccount, repo.equalize5h, repo.equalize7d)
	}
	if len(results) != 2 {
		t.Fatalf("results len = %d, want 2", len(results))
	}
}
