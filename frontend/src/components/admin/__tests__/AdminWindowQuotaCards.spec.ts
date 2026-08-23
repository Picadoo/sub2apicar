import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const apiMocks = vi.hoisted(() => ({
  getOverview: vi.fn(),
  rebalance: vi.fn(),
  setMembers: vi.fn(),
  setLimit: vi.fn(),
  listUsers: vi.fn(),
  getCeilings: vi.fn(),
  setSeats: vi.fn(),
  setCeiling: vi.fn(),
}))
const storeMocks = vi.hoisted(() => ({
  showSuccess: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api/accountWindowQuota', () => ({
  getAccountWindowQuotaOverview: apiMocks.getOverview,
  rebalanceAccountWindowQuotas: apiMocks.rebalance,
  setAccountWindowMembers: apiMocks.setMembers,
  setAccountWindowLimit: apiMocks.setLimit,
  getAccountWindowCeilings: apiMocks.getCeilings,
  setAccountWindowSeats: apiMocks.setSeats,
  setAccountWindowCeiling: apiMocks.setCeiling,
}))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    users: { list: apiMocks.listUsers },
  },
}))
vi.mock('@/stores/app', () => ({
  useAppStore: () => storeMocks,
}))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string | number>) => {
        if (!params) return key
        return `${key}:${JSON.stringify(params)}`
      },
    }),
  }
})

import AdminWindowQuotaConfigCard from '../AdminWindowQuotaConfigCard.vue'
import AdminWindowQuotaOverviewCard from '../AdminWindowQuotaOverviewCard.vue'

const overview = {
  enabled: true,
  rows: [
    {
      user_id: 1,
      email: 'a@example.com',
      username: 'A',
      account_id: 7,
      window_type: '5h',
      limit_percent: 48,
      used_percent: 10,
      remaining_percent: 38,
      donate_fraction: 0,
      effective_limit_percent: 48,
    },
    {
      user_id: 2,
      email: 'b@example.com',
      username: 'B',
      account_id: 7,
      window_type: '5h',
      limit_percent: 48,
      used_percent: 12,
      remaining_percent: 36,
      donate_fraction: 0,
      effective_limit_percent: 48,
    },
  ],
  summaries: [
    {
      account_id: 7,
      window_type: '5h',
      member_count: 2,
      configured_sum_percent: 96,
      used_sum_percent: 25,
      attributed_sum_percent: 22,
      unattributed_percent: 3,
      ceiling_percent: 92,
      overallocated: true,
    },
  ],
}

const memberOverview = {
  enabled: true,
  account_ids: [7],
  rows: [
    {
      user_id: 1,
      email: 'a@example.com',
      username: 'A',
      account_id: 7,
      window_type: '5h',
      limit_percent: 72,
      used_percent: 10,
      remaining_percent: 62,
      donate_fraction: 0,
      effective_limit_percent: 72,
    },
    {
      user_id: 1,
      email: 'a@example.com',
      username: 'A',
      account_id: 7,
      window_type: '7d',
      limit_percent: 72,
      used_percent: 10,
      remaining_percent: 62,
      donate_fraction: 0,
      effective_limit_percent: 72,
    },
    {
      user_id: 2,
      email: 'b@example.com',
      username: 'B',
      account_id: 7,
      window_type: '5h',
      limit_percent: 24,
      used_percent: 4,
      remaining_percent: 20,
      donate_fraction: 0,
      effective_limit_percent: 24,
    },
    {
      user_id: 2,
      email: 'b@example.com',
      username: 'B',
      account_id: 7,
      window_type: '7d',
      limit_percent: 24,
      used_percent: 4,
      remaining_percent: 20,
      donate_fraction: 0,
      effective_limit_percent: 24,
    },
  ],
  summaries: [
    {
      account_id: 7,
      window_type: '5h',
      member_count: 2,
      configured_sum_percent: 96,
      used_sum_percent: 14,
      attributed_sum_percent: 14,
      unattributed_percent: 0,
      ceiling_percent: 96,
      overallocated: false,
    },
    {
      account_id: 7,
      window_type: '7d',
      member_count: 2,
      configured_sum_percent: 96,
      used_sum_percent: 14,
      attributed_sum_percent: 14,
      unattributed_percent: 0,
      ceiling_percent: 96,
      overallocated: false,
    },
  ],
}

describe('AdminWindowQuotaOverviewCard', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    apiMocks.getOverview.mockResolvedValue(overview)
    apiMocks.getCeilings.mockResolvedValue({
      enabled: true,
      seats: 4,
      ceilings: [
        { window_type: '5h', ceiling_percent: 92 },
        { window_type: '7d', ceiling_percent: 92 },
      ],
    })
    apiMocks.rebalance.mockResolvedValue({ account_id: 7, updated_rows: 4 })
    apiMocks.setMembers.mockResolvedValue({ ok: true, account_id: 7, member_count: 3 })
    apiMocks.setLimit.mockResolvedValue(undefined)
    apiMocks.listUsers.mockResolvedValue({
      items: [
        { id: 1, username: 'A', email: 'a@example.com' },
        { id: 2, username: 'B', email: 'b@example.com' },
        { id: 3, username: 'Unused', email: 'unused@example.com' },
      ],
      total: 3,
      page: 1,
      page_size: 500,
      pages: 1,
    })
  })

  it('shows backend allocation diagnostics and overallocated warning', async () => {
    const wrapper = mount(AdminWindowQuotaOverviewCard)
    await flushPromises()

    expect(wrapper.text()).toContain('96%')
    expect(wrapper.text()).toContain('92%')
    expect(wrapper.text()).toContain('admin.windowQuotaOverview.official')
    expect(wrapper.text()).toContain('25%')
    expect(wrapper.text()).toContain('admin.windowQuotaOverview.attributedSum')
    expect(wrapper.text()).toContain('22%')
    expect(wrapper.text()).toContain('admin.windowQuotaOverview.unattributed')
    expect(wrapper.text()).toContain('3%')
    expect(wrapper.text()).toContain('admin.windowQuotaOverview.attributed 10%')
    expect(wrapper.text()).toContain('admin.windowQuotaOverview.remaining 38%')
    expect(wrapper.text()).toContain('admin.windowQuotaOverview.overallocatedWarning')
  })

  it('renders invalid member metrics as em dashes with a neutral progress bar', async () => {
    apiMocks.getOverview.mockResolvedValue({
      enabled: true,
      rows: [
        {
          user_id: 1,
          email: 'a@example.com',
          username: 'A',
          account_id: 7,
          window_type: '5h',
          limit_percent: Number.POSITIVE_INFINITY,
          used_percent: Number.NaN,
          remaining_percent: Number.NEGATIVE_INFINITY,
          donate_fraction: 0,
          effective_limit_percent: Number.NaN,
        },
      ],
      summaries: [],
    })

    const wrapper = mount(AdminWindowQuotaOverviewCard)
    await flushPromises()

    expect(wrapper.text()).toContain('—')
    const progress = wrapper.get('[data-testid="admin-window-quota-progress"]')
    expect(progress.classes()).toContain('bg-gray-400')
    expect(progress.attributes('style')).toContain('width: 0%')
  })

  it('shows per-window donor badges including 7d donations', async () => {
    apiMocks.getOverview.mockResolvedValue({
      enabled: true,
      rows: [
        {
          user_id: 1,
          email: 'a@example.com',
          username: 'A',
          account_id: 7,
          window_type: '5h',
          limit_percent: 23,
          used_percent: 0,
          remaining_percent: 23,
          donate_fraction: 0.5,
        },
        {
          user_id: 1,
          email: 'a@example.com',
          username: 'A',
          account_id: 7,
          window_type: '7d',
          limit_percent: 23,
          used_percent: 0,
          remaining_percent: 23,
          donate_fraction: 1,
        },
      ],
      summaries: [],
    })

    const wrapper = mount(AdminWindowQuotaOverviewCard)
    await flushPromises()

    expect(wrapper.text()).toContain('admin.windowQuotaOverview.donor:{"window":"5h","pct":50}')
    expect(wrapper.text()).toContain('admin.windowQuotaOverview.donor:{"window":"7d","pct":100}')
  })

  it('previews safe equal values, confirms, rebalances and refreshes', async () => {
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true)
    const wrapper = mount(AdminWindowQuotaOverviewCard)
    await flushPromises()

    const rebalanceButton = wrapper.findAll('button').find((button) =>
      button.text() === 'admin.windowQuotaOverview.rebalance'
    )
    await rebalanceButton!.trigger('click')
    expect(wrapper.get('[data-testid="rebalance-preview"]').text()).toContain('46')

    const confirmButton = wrapper.findAll('button').find((button) =>
      button.text() === 'admin.windowQuotaOverview.confirmRebalance'
    )
    await confirmButton!.trigger('click')
    await flushPromises()

    expect(confirmSpy).toHaveBeenCalledTimes(1)
    expect(apiMocks.rebalance).toHaveBeenCalledWith({ account_id: 7 })
    expect(apiMocks.getOverview).toHaveBeenCalledTimes(2)
    expect(storeMocks.showSuccess).toHaveBeenCalled()
    confirmSpy.mockRestore()
  })

  it('shows current 5h / 7d limits and creates editable defaults for a newly selected member', async () => {
    apiMocks.getOverview.mockResolvedValue(memberOverview)
    const wrapper = mount(AdminWindowQuotaOverviewCard)
    await flushPromises()

    const manageButton = wrapper.findAll('button').find((button) =>
      button.text() === 'admin.windowQuotaOverview.manageMembers'
    )
    await manageButton!.trigger('click')
    await flushPromises()

    expect((wrapper.get('[data-testid="member-limit-1-5h"]').element as HTMLInputElement).value).toBe('72')
    expect((wrapper.get('[data-testid="member-limit-1-7d"]').element as HTMLInputElement).value).toBe('72')
    expect((wrapper.get('[data-testid="member-limit-2-5h"]').element as HTMLInputElement).value).toBe('24')
    expect((wrapper.get('[data-testid="member-limit-2-7d"]').element as HTMLInputElement).value).toBe('24')

    await wrapper.get('[data-testid="member-checkbox-3"]').setValue(true)
    expect((wrapper.get('[data-testid="member-limit-3-5h"]').element as HTMLInputElement).value).toBe('24')
    expect((wrapper.get('[data-testid="member-limit-3-7d"]').element as HTMLInputElement).value).toBe('24')
    expect(wrapper.get('[data-testid="member-allocation-5h"]').text()).toContain('120')
  })

  it('saves custom member limits after membership sync, applying reductions before increases', async () => {
    apiMocks.getOverview.mockResolvedValue(memberOverview)
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true)
    const wrapper = mount(AdminWindowQuotaOverviewCard)
    await flushPromises()

    const manageButton = wrapper.findAll('button').find((button) =>
      button.text() === 'admin.windowQuotaOverview.manageMembers'
    )
    await manageButton!.trigger('click')
    await flushPromises()

    await wrapper.get('[data-testid="member-limit-1-5h"]').setValue('24')
    await wrapper.get('[data-testid="member-limit-1-7d"]').setValue('24')
    await wrapper.get('[data-testid="member-limit-2-5h"]').setValue('72')
    await wrapper.get('[data-testid="member-limit-2-7d"]').setValue('72')

    const saveButton = wrapper.findAll('button').find((button) =>
      button.text() === 'admin.windowQuotaOverview.saveMembers'
    )
    await saveButton!.trigger('click')
    await flushPromises()

    expect(apiMocks.setMembers).toHaveBeenCalledWith({ account_id: 7, user_ids: [1, 2] })
    expect(apiMocks.setLimit.mock.calls.map(([payload]) => payload)).toEqual([
      { user_id: 1, account_id: 7, window_type: '5h', limit_percent: 24 },
      { user_id: 1, account_id: 7, window_type: '7d', limit_percent: 24 },
      { user_id: 2, account_id: 7, window_type: '5h', limit_percent: 72 },
      { user_id: 2, account_id: 7, window_type: '7d', limit_percent: 72 },
    ])
    expect(apiMocks.setMembers.mock.invocationCallOrder[0]).toBeLessThan(
      apiMocks.setLimit.mock.invocationCallOrder[0],
    )
    expect(apiMocks.getOverview).toHaveBeenCalledTimes(3)
    expect(storeMocks.showSuccess).toHaveBeenCalled()
    confirmSpy.mockRestore()
  })

  it('rejects a custom allocation that exceeds the account ceiling', async () => {
    apiMocks.getOverview.mockResolvedValue(memberOverview)
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true)
    const wrapper = mount(AdminWindowQuotaOverviewCard)
    await flushPromises()

    const manageButton = wrapper.findAll('button').find((button) =>
      button.text() === 'admin.windowQuotaOverview.manageMembers'
    )
    await manageButton!.trigger('click')
    await flushPromises()

    await wrapper.get('[data-testid="member-limit-1-5h"]').setValue('80')
    const saveButton = wrapper.findAll('button').find((button) =>
      button.text() === 'admin.windowQuotaOverview.saveMembers'
    )
    await saveButton!.trigger('click')
    await flushPromises()

    expect(apiMocks.setMembers).not.toHaveBeenCalled()
    expect(apiMocks.setLimit).not.toHaveBeenCalled()
    expect(confirmSpy).not.toHaveBeenCalled()
    expect(storeMocks.showError).toHaveBeenCalledWith(
      expect.stringContaining('admin.windowQuotaOverview.memberCeilingExceeded'),
    )
    confirmSpy.mockRestore()
  })
})

describe('AdminWindowQuotaConfigCard', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    apiMocks.getCeilings.mockResolvedValue({
      enabled: true,
      seats: 4,
      ceilings: [
        { window_type: '5h', ceiling_percent: 92 },
        { window_type: '7d', ceiling_percent: 92 },
      ],
    })
    apiMocks.setSeats.mockResolvedValue(undefined)
    apiMocks.setCeiling.mockResolvedValue(undefined)
  })

  it('labels seats as a future-record default and reads back after saving', async () => {
    const wrapper = mount(AdminWindowQuotaConfigCard)
    await flushPromises()

    expect(wrapper.text()).toContain('admin.windowQuotaConfig.seatsLabel')
    expect(wrapper.text()).toContain('admin.windowQuotaConfig.note')
    const input = wrapper.get('input[type="number"]')
    await input.setValue('5')
    const saveButton = wrapper.findAll('button')[0]
    await saveButton.trigger('click')
    await flushPromises()

    expect(apiMocks.setSeats).toHaveBeenCalledWith(5)
    expect(apiMocks.getCeilings).toHaveBeenCalledTimes(2)
  })
})
