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
    expect(wrapper.text()).toContain('Alice')
    expect(wrapper.text()).toContain('Bob')
    expect(wrapper.text()).toContain('5h: 5% / 46%')
    expect(wrapper.text()).toContain('5h: 8% / 46%')
  })
})
