import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post, put } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
}))

vi.mock('../client', () => ({
  apiClient: { get, post, put },
}))

import {
  getAccountWindowQuotaOverview,
  rebalanceAccountWindowQuotas,
  setAccountWindowMembers,
} from '@/api/accountWindowQuota'

describe('account window quota admin API', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
    put.mockReset()
  })

  it('uses the admin overview endpoint and returns summaries', async () => {
    const response = {
      enabled: true,
      rows: [],
      summaries: [{
        account_id: 7,
        window_type: '5h',
        member_count: 4,
        configured_sum_percent: 96,
        used_sum_percent: 20,
        attributed_sum_percent: 18,
        unattributed_percent: 2,
        ceiling_percent: 92,
        overallocated: true,
      }],
    }
    get.mockResolvedValue({ data: response })

    await expect(getAccountWindowQuotaOverview()).resolves.toEqual(response)
    expect(get).toHaveBeenCalledWith('/admin/account-window-quotas/overview')
  })

  it('puts an explicit member list without requiring a legacy rebalance payload', async () => {
    const response = { ok: true, account_id: 7, member_count: 3 }
    put.mockResolvedValue({ data: response })

    await expect(
      setAccountWindowMembers({ account_id: 7, user_ids: [1, 2, 3] }),
    ).resolves.toEqual(response)

    expect(put).toHaveBeenCalledWith('/admin/account-window-quotas/accounts/7/members', {
      user_ids: [1, 2, 3],
    })
  })

  it('posts to the account equalize endpoint', async () => {
    post.mockResolvedValue({
      data: {
        ok: true,
        account_id: 7,
        windows: [{ window_type: '5h', member_count: 4, share_percent: 23, ceiling_percent: 92 }],
      },
    })

    await rebalanceAccountWindowQuotas({ account_id: 7 })

    expect(post).toHaveBeenCalledWith('/admin/account-window-quotas/accounts/7/equalize')
  })
})
