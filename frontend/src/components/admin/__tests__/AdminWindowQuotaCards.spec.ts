import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const apiMocks = vi.hoisted(() => ({
  getOverview: vi.fn(),
  rebalance: vi.fn(),
  setMembers: vi.fn(),
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
    },
  ],
  summaries: [
    {
      account_id: 7,
      window_type: '5h',
      member_count: 2,
      configured_sum_percent: 96,
      used_sum_percent: 22,
      ceiling_percent: 92,
      overallocated: true,
    },
  ],
}

describe('AdminWindowQuotaOverviewCard', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    apiMocks.getOverview.mockResolvedValue(overview)
    apiMocks.rebalance.mockResolvedValue({ account_id: 7, updated_rows: 4 })
    apiMocks.setMembers.mockResolvedValue({ account_id: 7, windows: [] })
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
    expect(wrapper.text()).toContain('admin.windowQuotaOverview.overallocatedWarning')
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

  it('lets the admin add an unused user to the explicit member list', async () => {
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true)
    const wrapper = mount(AdminWindowQuotaOverviewCard)
    await flushPromises()

    const manageButton = wrapper.findAll('button').find((button) =>
      button.text() === 'admin.windowQuotaOverview.manageMembers'
    )
    await manageButton!.trigger('click')
    await flushPromises()

    const checkboxes = wrapper.get('[data-testid="member-editor"]').findAll('input[type="checkbox"]')
    expect(checkboxes).toHaveLength(3)
    await checkboxes[2].setValue(true)

    const saveButton = wrapper.findAll('button').find((button) =>
      button.text() === 'admin.windowQuotaOverview.saveMembers'
    )
    await saveButton!.trigger('click')
    await flushPromises()

    expect(apiMocks.setMembers).toHaveBeenCalledWith({ account_id: 7, user_ids: [1, 2, 3] })
    expect(apiMocks.getOverview).toHaveBeenCalledTimes(2)
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
