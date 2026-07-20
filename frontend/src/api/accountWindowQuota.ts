/**
 * Account window percentage quota API.
 *
 * 每个用户在「某账号 × 某官方窗口（5h / 7d）」上可占用的官方利用率百分比上限
 * （默认 23%）。用户侧查看自己的已用 / 剩余百分比与重置时间；管理侧覆盖上限。
 */

import { apiClient } from './client'

export interface AccountWindowQuotaItem {
  account_id: number
  window_type: string // '5h' | '7d'
  limit_percent: number
  used_percent: number
  remaining_percent: number
  /** 本窗口救急池捐赠比例（占自己份额，0~1）；5h / 7d 各自独立。 */
  donate_fraction: number
  /** 已被其他成员借用后，本窗口不可撤回的最低捐赠比例（0~1）。 */
  minimum_donate_fraction?: number
  /** 当前实际可用上限（含救急池增量 / 捐赠自留约束）。 */
  effective_limit_percent: number
  /** 该账号该窗口尚未被借走的救急池余额。 */
  pool_available_percent: number
  /** 该账号该窗口全员已用之和（账号级利用率%）。 */
  account_used_percent?: number
  /** 该窗口账号总额上限（官方安全水位%）。 */
  ceiling_percent?: number
  window_reset_at?: string
  reset_in_seconds?: number
}

export interface AccountWindowQuotasResponse {
  enabled: boolean
  windows: AccountWindowQuotaItem[]
  /** 当前用户参与的拼车账号内，全体成员额度。 */
  members?: AdminWindowQuotaOverviewItem[]
  /** 当前用户参与的拼车账号汇总。 */
  summaries?: AdminWindowQuotaSummary[]
}

/** 获取当前登录用户的全部窗口百分比配额。 */
export async function getMyAccountWindowQuotas(): Promise<AccountWindowQuotasResponse> {
  const { data } = await apiClient.get<AccountWindowQuotasResponse>('/user/account-window-quotas')
  return data
}

/** 管理端：获取指定用户的全部窗口百分比配额（用户配额弹窗中展示）。 */
export async function getUserAccountWindowQuotas(
  userId: number,
): Promise<AccountWindowQuotasResponse> {
  const { data } = await apiClient.get<AccountWindowQuotasResponse>(
    `/admin/account-window-quotas/users/${userId}`,
  )
  return data
}

export interface SetAccountWindowLimitPayload {
  user_id: number
  account_id: number
  window_type: string
  limit_percent: number
}

/** 管理端：覆盖某 (user, account, window) 的 limit_percent。 */
export async function setAccountWindowLimit(payload: SetAccountWindowLimitPayload): Promise<void> {
  await apiClient.post('/admin/account-window-quotas/limit', payload)
}

export interface AccountWindowCeilingItem {
  window_type: string // '5h' | '7d'
  ceiling_percent: number
}

export interface AccountWindowCeilingsResponse {
  enabled: boolean
  ceilings: AccountWindowCeilingItem[]
  /** 车位数（共享人数）。 */
  seats?: number
  /** 人均默认上限 = ceiling/seats。 */
  default_limit_percent?: number
}

/** 管理端：获取各官方窗口的总额上限（所有用户 limit 之和的上限，默认 92%）+ 车位数。 */
export async function getAccountWindowCeilings(): Promise<AccountWindowCeilingsResponse> {
  const { data } = await apiClient.get<AccountWindowCeilingsResponse>('/admin/account-window-quotas/ceilings')
  return data
}

/** 管理端：设置车位数（共享人数）。换 3/5/8 人车只改这个，人均默认 = ceiling/seats 自动适配。 */
export async function setAccountWindowSeats(seats: number): Promise<void> {
  await apiClient.post('/admin/account-window-quotas/seats', { seats })
}

export interface SetAccountWindowCeilingPayload {
  window_type: string
  ceiling_percent: number
}

/** 管理端：设置某官方窗口的总额上限（运行时可配）。 */
export async function setAccountWindowCeiling(payload: SetAccountWindowCeilingPayload): Promise<void> {
  await apiClient.post('/admin/account-window-quotas/ceiling', payload)
}

export interface SetAccountWindowDonatePayload {
  account_id: number
  window_type: string // '5h' | '7d'
  fraction: number // 占自己该窗口份额的捐赠比例，0~1
}

/** 用户自助：设置自己在某账号某窗口（5h/7d）的「救急池」捐赠比例（0~1）。 */
export async function setAccountWindowDonate(payload: SetAccountWindowDonatePayload): Promise<void> {
  await apiClient.post('/user/account-window-quotas/donate', payload)
}

export interface AdminWindowQuotaOverviewItem {
  user_id: number
  email: string
  username: string
  account_id: number
  window_type: string
  limit_percent: number
  used_percent: number
  remaining_percent: number
  /** 本窗口救急池捐赠比例（5h/7d 各自独立，0~1）。 */
  donate_fraction: number
  /** 含救急池增量/捐赠自留后的有效上限%。 */
  effective_limit_percent?: number
  /** 该账号该窗口救急池当前可借总额%。 */
  pool_available_percent?: number
  window_reset_at?: string
  reset_in_seconds?: number
}

export interface AdminWindowQuotaSummary {
  account_id: number
  window_type: string
  member_count: number
  /** 当前成员已配置 limit_percent 之和。 */
  configured_sum_percent: number
  /** 当前成员已用官方窗口百分比之和。 */
  used_sum_percent: number
  /** 该账号该窗口允许分配的安全总额。 */
  ceiling_percent: number
  /** configured_sum_percent 是否超过 ceiling_percent。 */
  overallocated: boolean
}

export interface AdminWindowQuotaOverviewResponse {
  enabled: boolean
  rows: AdminWindowQuotaOverviewItem[]
  summaries: AdminWindowQuotaSummary[]
  /** 已启用拼车额度的账号，包括尚无成员额度行的账号。 */
  account_ids?: number[]
}

export interface RebalanceAccountWindowQuotasPayload {
  account_id: number
}

export interface EqualizedAccountWindowItem {
  window_type: string
  member_count: number
  share_percent: number
  ceiling_percent: number
}

export interface RebalanceAccountWindowQuotasResponse {
  ok: boolean
  account_id: number
  windows: EqualizedAccountWindowItem[]
}

/** 管理端：所有用户在所有账号所有窗口的配额总览与分配诊断。 */
export async function getAccountWindowQuotaOverview(): Promise<AdminWindowQuotaOverviewResponse> {
  const { data } = await apiClient.get<AdminWindowQuotaOverviewResponse>(
    '/admin/account-window-quotas/overview',
  )
  return data
}

export interface SetAccountWindowMembersPayload {
  account_id: number
  user_ids: number[]
}

/** 管理端：显式指定拼车成员；未使用用户也会创建额度并立即均分。 */
export async function setAccountWindowMembers(
  payload: SetAccountWindowMembersPayload,
): Promise<RebalanceAccountWindowQuotasResponse> {
  const { data } = await apiClient.put<RebalanceAccountWindowQuotasResponse>(
    `/admin/account-window-quotas/accounts/${payload.account_id}/members`,
    { user_ids: payload.user_ids },
  )
  return data
}

/** 管理端：按账号当前成员，将 5h / 7d 配额安全均分。 */
export async function rebalanceAccountWindowQuotas(
  payload: RebalanceAccountWindowQuotasPayload,
): Promise<RebalanceAccountWindowQuotasResponse> {
  const { data } = await apiClient.post<RebalanceAccountWindowQuotasResponse>(
    `/admin/account-window-quotas/accounts/${payload.account_id}/equalize`,
  )
  return data
}

export const accountWindowQuotaAPI = {
  getMyAccountWindowQuotas,
  getUserAccountWindowQuotas,
  getAccountWindowQuotaOverview,
  setAccountWindowMembers,
  rebalanceAccountWindowQuotas,
  setAccountWindowLimit,
  getAccountWindowCeilings,
  setAccountWindowCeiling,
  setAccountWindowSeats,
  setAccountWindowDonate,
}

export default accountWindowQuotaAPI
