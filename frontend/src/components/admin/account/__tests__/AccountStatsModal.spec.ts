import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AccountStatsModal from '../AccountStatsModal.vue'
import type { Account } from '@/types'
const mocks = vi.hoisted(() => ({ getStats: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: mocks } }))
vi.mock('vue-i18n', async (importOriginal) => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)
beforeEach(() => vi.clearAllMocks())
const response = (requests: number) => ({ history: [], models: [], summary: {
 total_requests: requests, total_cost: 0, total_user_cost: 0, total_standard_cost: 0,
 avg_daily_cost: 0, avg_daily_user_cost: 0, avg_daily_requests: 0, avg_daily_tokens: 0,
 total_tokens: 0, actual_days_used: 1, avg_duration_ms: 0
} })
function open() {
 return mount(AccountStatsModal, { props: { show: true, account: { id: 29, name: 'test', status: 'active' } as Account }, global: { stubs: {
 BaseDialog: { template: '<div><slot /></div>' }, ModelDistributionChart: true,
 EndpointDistributionChart: true, Line: true
 } } })
}
describe('account stats period', () => {
 it('defaults to all history, displays exact requests, and allows 30 days', async () => {
  mocks.getStats.mockResolvedValueOnce(response(12345)).mockResolvedValueOnce(response(123))
  const w = open(); await flushPromises()
  expect(mocks.getStats).toHaveBeenLastCalledWith(29, 'all')
  expect(w.text()).toContain('admin.accounts.stats.totalRequestsAllTime')
  expect(w.text()).toContain((12345).toLocaleString())
  await w.get('select').setValue(30); await flushPromises()
  expect(mocks.getStats).toHaveBeenLastCalledWith(29, 30)
  expect(w.text()).toContain('admin.accounts.stats.totalRequests')
  expect(w.text()).not.toContain('admin.accounts.stats.totalRequestsAllTime')
 })
 it('ignores an older period response after switching', async () => {
  let resolve!: (v: unknown) => void
  mocks.getStats.mockReturnValueOnce(new Promise(r => { resolve = r })).mockResolvedValueOnce(response(123))
  const w = open()
  await w.get('select').setValue(30); await flushPromises()
  resolve(response(98765)); await flushPromises()
  expect(w.text()).toContain('123')
  expect(w.text()).not.toContain((98765).toLocaleString())
 })
})
