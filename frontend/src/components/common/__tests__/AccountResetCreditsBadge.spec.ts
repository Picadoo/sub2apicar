import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountResetCreditsBadge from '../AccountResetCreditsBadge.vue'
import zhDashboard from '@/i18n/locales/zh/dashboard'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => {
      const parts = key.split('.')
      let cur: unknown = { dashboard: zhDashboard.dashboard }
      for (const p of parts) {
        cur = (cur as Record<string, unknown> | undefined)?.[p]
      }
      let text = typeof cur === 'string' ? cur : key
      for (const [k, v] of Object.entries(params ?? {})) {
        text = text.replace(`{${k}}`, String(v))
      }
      return text
    },
  }),
}))

describe('AccountResetCreditsBadge', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-11-26T08:00:00Z'))
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('shows count and earliest expiration and expands multiple credits', async () => {
    const wrapper = mount(AccountResetCreditsBadge, {
      props: {
        resetCredits: {
          available_count: 3,
          credits: [
            { expires_at: '2026-11-28T11:00:00Z' },
            { expires_at: '2026-12-01T08:00:00Z' },
            { expires_at: '2026-12-10T08:00:00Z' },
          ],
        },
      },
    })

    const summary = wrapper.get('[data-testid="carpool-reset-credits-summary"]').text()
    expect(summary).toContain('重置卡 3 张')
    expect(summary).toContain('最早 2天3小时后过期')

    const toggle = wrapper.get('[data-testid="carpool-reset-credits-toggle"]')
    expect(toggle.text()).toBe('+2')
    expect(wrapper.find('[data-testid="carpool-reset-credits-details"]').exists()).toBe(false)

    await toggle.trigger('click')
    const details = wrapper.get('[data-testid="carpool-reset-credits-details"]').text()
    expect(details).toContain('第 1 张')
    expect(details).toContain('第 2 张')
    expect(details).toContain('第 3 张')
  })

  it('shows explicit zero when available_count is 0', () => {
    const wrapper = mount(AccountResetCreditsBadge, {
      props: { resetCredits: { available_count: 0, credits: [] } },
    })
    expect(wrapper.get('[data-testid="carpool-reset-credits-summary"]').text()).toBe('重置卡 0 张')
    expect(wrapper.find('[data-testid="carpool-reset-credits-toggle"]').exists()).toBe(false)
  })

  it('shows unknown label or hides when showUnknown is false', () => {
    const visibleWrapper = mount(AccountResetCreditsBadge, {
      props: { resetCredits: null, showUnknown: true },
    })
    expect(visibleWrapper.get('[data-testid="carpool-reset-credits-summary"]').text()).toBe('重置卡 暂未查询')

    const hiddenWrapper = mount(AccountResetCreditsBadge, {
      props: { resetCredits: null, showUnknown: false },
    })
    expect(hiddenWrapper.find('[data-testid="carpool-reset-credits"]').exists()).toBe(false)
  })
})
