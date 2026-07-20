package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
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

// stubWindowRepo 是 UserAccountWindowQuotaRepository 的最小桩，仅 ListByAccount/ListByUser 返回固定数据。
type stubWindowRepo struct {
	rows             []UserAccountWindowQuotaRecord
	adminRows        []AdminWindowQuotaOverviewRow
	setCalls         []UserAccountWindowQuotaRecord
	maxConfigured    float64
	maxConfiguredErr error
	equalizeResults  []AccountWindowEqualizationResult
	equalizeErr      error
	equalizeAccount  int64
	equalize5h       float64
	equalize7d       float64
}

func (s *stubWindowRepo) ResetWindowForAccount(context.Context, int64, string, *time.Time) error {
	return nil
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
func (s *stubWindowRepo) RecomputeWindowShares(context.Context, int64, string, float64, time.Time, *time.Time, float64) error {
	return nil
}
func (s *stubWindowRepo) ListAllWithUser(context.Context) ([]AdminWindowQuotaOverviewRow, error) {
	return s.adminRows, nil
}
func (s *stubWindowRepo) SetDonatePoolFraction(context.Context, int64, int64, string, float64, float64) error {
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
func (s *stubWindowRepo) SyncAccountMembers(_ context.Context, accountID int64, _ []int64, ceiling5h, ceiling7d float64) ([]AccountWindowEqualizationResult, error) {
	s.equalizeAccount = accountID
	s.equalize5h = ceiling5h
	s.equalize7d = ceiling7d
	return s.equalizeResults, s.equalizeErr
}

func newStubQuotaService(rows []UserAccountWindowQuotaRecord) *AccountWindowQuotaService {
	return NewAccountWindowQuotaService(&stubWindowRepo{rows: rows}, redis.NewClient(&redis.Options{}))
}

// 顶满自己 5h 份额、但救急池有余 → 放行（借用救急池）。
func TestCheckUserAccountEligible_PoolAllowsBorrow(t *testing.T) {
	svc := newStubQuotaService([]UserAccountWindowQuotaRecord{
		awqRec(1, WindowType5h, 23, 23, 0),
		awqRec(2, WindowType5h, 23, 0, 1),
	})
	eligible, _, _ := svc.CheckUserAccountEligible(context.Background(), 1, 1)
	if !eligible {
		t.Fatalf("alice should be eligible via emergency pool")
	}
}

// 7d 周配额打满 → 即使 5h 救急池有余也拦截（铁底线）。
func TestCheckUserAccountEligible_SevenDayHardBlock(t *testing.T) {
	svc := newStubQuotaService([]UserAccountWindowQuotaRecord{
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
	svc := newStubQuotaService([]UserAccountWindowQuotaRecord{
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
	svc := newStubQuotaService([]UserAccountWindowQuotaRecord{
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
	svc := newStubQuotaService([]UserAccountWindowQuotaRecord{
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

// 账号级硬闸：某窗口全员已用之和达到 ceiling → 即使个人远未到自己上限也全员拦截（人数无关的安全网）。
func TestCheckEligible_AccountCeilingHardStop(t *testing.T) {
	svc := newStubQuotaService([]UserAccountWindowQuotaRecord{
		awqRec(1, WindowType5h, 50, 10, 0), // user1 远未到自己 50 的上限
		awqRec(2, WindowType5h, 50, 85, 0), // Σ5h=95 ≥ 92 ceiling
	})
	eligible, window, _ := svc.CheckUserAccountEligible(context.Background(), 1, 1)
	if eligible || window != WindowType5h {
		t.Fatalf("should hard-stop at account ceiling, got eligible=%v window=%q", eligible, window)
	}
}

func newQuotaServiceWithMiniRedis(t *testing.T, repo UserAccountWindowQuotaRepository) (*AccountWindowQuotaService, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewAccountWindowQuotaService(repo, rdb), mr
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
	repo := &stubWindowRepo{adminRows: []AdminWindowQuotaOverviewRow{
		{UserID: 1, AccountID: 7, WindowType: WindowType5h, LimitPercent: 30, AttributedPercent: 10},
		{UserID: 2, AccountID: 7, WindowType: WindowType5h, LimitPercent: 30, AttributedPercent: 5},
		{UserID: 1, AccountID: 7, WindowType: WindowType7d, LimitPercent: 40, AttributedPercent: 20},
		{UserID: 2, AccountID: 7, WindowType: WindowType7d, LimitPercent: 30, AttributedPercent: 15},
	}}
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
		!awqApproxEq(got.ConfiguredSumPercent, 60) || !awqApproxEq(got.UsedSumPercent, 15) ||
		!awqApproxEq(got.CeilingPercent, 50) || !got.Overallocated {
		t.Fatalf("5h summary = %+v", got)
	}
	if got := summaries[1]; got.AccountID != 7 || got.WindowType != WindowType7d || got.MemberCount != 2 ||
		!awqApproxEq(got.ConfiguredSumPercent, 70) || !awqApproxEq(got.UsedSumPercent, 35) ||
		!awqApproxEq(got.CeilingPercent, 80) || got.Overallocated {
		t.Fatalf("7d summary = %+v", got)
	}
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
