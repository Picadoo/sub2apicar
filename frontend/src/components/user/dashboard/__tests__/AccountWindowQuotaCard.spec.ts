import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const apiMocks = vi.hoisted(() => ({
  getMy: vi.fn(),
  donate: vi.fn(),
}))

vi.mock('@/api/accountWindowQuota', () => ({
  getMyAccountWindowQuotas: apiMocks.getMy,
  setAccountWindowDonate: apiMocks.donate,
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string | number>) =>
        params ? `${key}:${JSON.stringify(params)}` : key,
    }),
  }
})

import AccountWindowQuotaCard from '../AccountWindowQuotaCard.vue'

describe('AccountWindowQuotaCard', () => {
  it('shows every member quota for the current shared account', async () => {
    apiMocks.getMy.mockResolvedValue({
      enabled: true,
      windows: [
        {
          account_id: 7,
          window_type: '5h',
          limit_percent: 46,
          used_percent: 5,
          remaining_percent: 41,
          donate_fraction: 0,
          effective_limit_percent: 46,
          pool_available_percent: 0,
          account_used_percent: 13,
          account_attributed_percent: 12,
          account_unattributed_percent: 1,
          ceiling_percent: 92,
        },
      ],
      members: [
        { user_id: 1, username: 'Alice', email: 'a@example.com', account_id: 7, window_type: '5h', limit_percent: 46, used_percent: 5, remaining_percent: 41, donate_fraction: 0 },
        { user_id: 2, username: 'Bob', email: 'b@example.com', account_id: 7, window_type: '5h', limit_percent: 46, used_percent: 8, remaining_percent: 38, donate_fraction: 0 },
      ],
    })

    const wrapper = mount(AccountWindowQuotaCard)
    await flushPromises()

    expect(wrapper.text()).toContain('dashboard.accountWindowQuota.membersTitle')
    expect(wrapper.text()).toContain('dashboard.accountWindowQuota.officialUsed')
    expect(wrapper.text()).toContain('dashboard.accountWindowQuota.accountCeiling')
    expect(wrapper.text()).toContain('dashboard.accountWindowQuota.siteAttributed')
    expect(wrapper.text()).toContain('dashboard.accountWindowQuota.personalLimit')
    expect(wrapper.text()).toContain('Alice')
    expect(wrapper.text()).toContain('Bob')
    expect(wrapper.text()).toContain(
      '5h: dashboard.accountWindowQuota.siteAttributed 5% / dashboard.accountWindowQuota.personalLimit 46%',
    )
    expect(wrapper.text()).toContain(
      '5h: dashboard.accountWindowQuota.siteAttributed 8% / dashboard.accountWindowQuota.personalLimit 46%',
    )
    expect(wrapper.get('[data-testid="current-window-availability"]').text()).toContain('41%')
    expect(wrapper.get('[data-testid="account-attribution-breakdown"]').text()).toContain(
      'dashboard.accountWindowQuota.unattributedNotice:{"pct":"1%"}',
    )
  })

  it('falls back to personal allowance when the official snapshot is missing', async () => {
    apiMocks.getMy.mockResolvedValue({
      enabled: true,
      windows: [
        {
          account_id: 7,
          window_type: '5h',
          limit_percent: 46,
          used_percent: 5,
          remaining_percent: 41,
          donate_fraction: 0,
          effective_limit_percent: 46,
          pool_available_percent: 0,
          ceiling_percent: 92,
        },
      ],
      members: [],
    })

    const wrapper = mount(AccountWindowQuotaCard)
    await flushPromises()

    expect(wrapper.get('[data-testid="current-window-availability"]').text()).toContain('41%')
    expect(wrapper.get('[data-testid="current-window-availability"]').text()).toContain(
      'dashboard.accountWindowQuota.availableEstimateHint',
    )
    const metrics = wrapper.get('[data-testid="window-quota-metrics"]')
    expect(metrics.text()).toContain('dashboard.accountWindowQuota.officialUsed --')
    expect(metrics.text()).toContain('dashboard.accountWindowQuota.accountCeiling 92%')
    expect(wrapper.get('[data-testid="account-attribution-breakdown"]').text()).toContain(
      'dashboard.accountWindowQuota.snapshotMissing',
    )
  })

  it('uses placeholders and neutral progress for invalid quota numbers', async () => {
    apiMocks.getMy.mockResolvedValue({
      enabled: true,
      windows: [
        {
          account_id: 7,
          window_type: '5h',
          limit_percent: Number.POSITIVE_INFINITY,
          used_percent: Number.NaN,
          remaining_percent: Number.NEGATIVE_INFINITY,
          donate_fraction: 0,
          effective_limit_percent: Number.NaN,
          pool_available_percent: 0,
          account_used_percent: 0,
          account_attributed_percent: 0,
          account_unattributed_percent: 0,
        },
      ],
      members: [],
    })

    const wrapper = mount(AccountWindowQuotaCard)
    await flushPromises()

    const metrics = wrapper.get('[data-testid="window-quota-metrics"]')
    expect(metrics.text()).toContain('--')
    expect(wrapper.get('[data-testid="current-window-availability"]').text()).toContain('--')
    const progress = wrapper.get('[data-testid="window-quota-progress"]')
    expect(progress.classes()).toContain('bg-gray-400')
    expect(progress.attributes('style')).toContain('width: 0%')
    expect(wrapper.get('[data-testid="account-attribution-breakdown"]').text()).toContain(
      'dashboard.accountWindowQuota.snapshotMissing',
    )
  })

  it('caps the displayed availability at account headroom and explains external usage', async () => {
    apiMocks.getMy.mockResolvedValue({
      enabled: true,
      windows: [
        {
          account_id: 29,
          window_type: '7d',
          limit_percent: 24,
          used_percent: 0,
          remaining_percent: 24,
          donate_fraction: 0,
          effective_limit_percent: 24,
          pool_available_percent: 0,
          account_used_percent: 83,
          account_attributed_percent: 0,
          account_unattributed_percent: 83,
          ceiling_percent: 92,
        },
      ],
      members: [],
    })

    const wrapper = mount(AccountWindowQuotaCard)
    await flushPromises()

    const availability = wrapper.get('[data-testid="current-window-availability"]')
    expect(availability.text()).toContain('dashboard.accountWindowQuota.availableNow')
    expect(availability.text()).toContain('9%')
    expect(availability.text()).toContain('dashboard.accountWindowQuota.availableSharedHint')

    const metrics = wrapper.get('[data-testid="window-quota-metrics"]')
    expect(metrics.text()).toContain('dashboard.accountWindowQuota.officialUsed 83%')
    expect(metrics.text()).toContain('dashboard.accountWindowQuota.accountCeiling 92%')
    expect(metrics.text()).toContain('dashboard.accountWindowQuota.siteAttributed 0%')
    expect(metrics.text()).toContain('dashboard.accountWindowQuota.personalLimit 24%')
    expect(metrics.text()).not.toContain('dashboard.accountWindowQuota.remaining')

    const progress = wrapper.get('[data-testid="window-quota-progress"]')
    expect(progress.classes()).toContain('bg-amber-500')
    expect(progress.attributes('style')).toContain('width: 90%')
    expect(wrapper.get('[data-testid="account-attribution-breakdown"]').text()).toContain(
      'dashboard.accountWindowQuota.unattributedNotice:{"pct":"83%"}',
    )
  })

  it('shows a force-unattributed notice and grey progress when the account attributes nothing to members', async () => {
    apiMocks.getMy.mockResolvedValue({
      enabled: true,
      windows: [
        {
          account_id: 29,
          window_type: '7d',
          limit_percent: 24,
          used_percent: 0,
          remaining_percent: 24,
          donate_fraction: 0,
          effective_limit_percent: 24,
          pool_available_percent: 0,
          account_used_percent: 83,
          account_attributed_percent: 0,
          account_unattributed_percent: 83,
          ceiling_percent: 92,
          force_unattributed: true,
        },
      ],
      members: [],
    })

    const wrapper = mount(AccountWindowQuotaCard)
    await flushPromises()

    expect(wrapper.get('[data-testid="account-attribution-breakdown"]').text()).toContain(
      'dashboard.accountWindowQuota.forceUnattributedNotice:{"pct":"83%"}',
    )
    // force 模式下当前最多可用仍受账号安全余量约束（83 vs 92 → 9%）。
    expect(wrapper.get('[data-testid="current-window-availability"]').text()).toContain('9%')
    // 官方快照存在：进度条按 official/ceiling 显示（83/92 ≈ 90%，amber）。
    const progress = wrapper.get('[data-testid="window-quota-progress"]')
    expect(progress.classes()).toContain('bg-amber-500')
    expect(progress.attributes('style')).toContain('width: 90%')
  })

  it('renders a neutral grey progress bar when the official snapshot is missing', async () => {
    apiMocks.getMy.mockResolvedValue({
      enabled: true,
      windows: [
        {
          account_id: 7,
          window_type: '5h',
          limit_percent: 46,
          used_percent: 5,
          remaining_percent: 41,
          donate_fraction: 0,
          effective_limit_percent: 46,
          pool_available_percent: 0,
          ceiling_percent: 92,
          // account_used_percent 缺失：官方快照暂缺。
        },
      ],
      members: [],
    })

    const wrapper = mount(AccountWindowQuotaCard)
    await flushPromises()

    const progress = wrapper.get('[data-testid="window-quota-progress"]')
    expect(progress.classes()).toContain('bg-gray-400')
    expect(progress.attributes('style')).toContain('width: 0%')
    expect(wrapper.get('[data-testid="account-attribution-breakdown"]').text()).toContain(
      'dashboard.accountWindowQuota.snapshotMissing',
    )
  })

  it('locks the slider above quota already borrowed by other members', async () => {
    apiMocks.donate.mockReset()
    apiMocks.donate.mockResolvedValue({ success: true })
    apiMocks.getMy.mockResolvedValue({
      enabled: true,
      windows: [
        {
          account_id: 7,
          window_type: '5h',
          limit_percent: 23,
          used_percent: 0,
          remaining_percent: 23,
          donate_fraction: 1,
          minimum_donate_fraction: 12 / 23,
          effective_limit_percent: 0,
          pool_available_percent: 11,
          account_used_percent: 35,
          ceiling_percent: 92,
        },
      ],
      members: [],
    })

    const wrapper = mount(AccountWindowQuotaCard)
    await flushPromises()

    const slider = wrapper.get('input[type="range"]')
    expect(Number(slider.attributes('min'))).toBeCloseTo((12 / 23) * 100)
    expect(wrapper.text()).toContain('dashboard.accountWindowQuota.lockedDonationHint')
    expect(wrapper.text()).toContain('"pct":"52.2"')
    // 全捐且未用：个人当前上限和当前最多可用都必须是 0，不能继续显示假的 23% 剩余。
    const metrics = wrapper.get('[data-testid="window-quota-metrics"]')
    expect(metrics.text()).toContain('dashboard.accountWindowQuota.siteAttributed 0%')
    expect(metrics.text()).toContain('dashboard.accountWindowQuota.personalLimit 0%')
    expect(metrics.text()).toContain('dashboard.accountWindowQuota.baseShare:{"pct":"23%"}')
    expect(wrapper.get('[data-testid="current-window-availability"]').text()).toContain('0%')
    expect(metrics.text()).not.toContain('dashboard.accountWindowQuota.remaining')

    await slider.setValue('0')
    await slider.trigger('change')
    await flushPromises()

    expect(apiMocks.donate).toHaveBeenLastCalledWith({
      account_id: 7,
      window_type: '5h',
      fraction: 12 / 23,
    })
  })
})
