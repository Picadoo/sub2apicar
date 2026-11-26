import type { AccountResetCreditsInfo } from '@/api/accountWindowQuota'

export interface NormalizedResetCreditItem {
  expiresAt: string
  remainingMs: number | null
}

export type ResetCreditUrgency = 'none' | 'normal' | 'soon' | 'urgent'

export interface NormalizedResetCredits {
  state: 'unknown' | 'empty' | 'available'
  count: number
  credits: NormalizedResetCreditItem[]
  urgency: ResetCreditUrgency
}

const HOUR_MS = 60 * 60 * 1000
const DAY_MS = 24 * HOUR_MS
const MINUTE_MS = 60 * 1000

/**
 * 按当前时间过滤已过期卡并排序，防止页面长时间停留时继续展示已过期卡。
 * 与后端和 OpenAIQuotaResetCell 口径一致：快照声称有卡但全部已过期时返回 unknown，不伪造 0 张。
 */
export function normalizeAccountResetCredits(
  info: AccountResetCreditsInfo | null | undefined,
  nowMs: number = Date.now(),
): NormalizedResetCredits {
  if (!info || typeof info.available_count !== 'number' || !Number.isFinite(info.available_count) || info.available_count < 0) {
    return { state: 'unknown', count: 0, credits: [], urgency: 'none' }
  }

  const rawCount = Math.floor(info.available_count)
  if (rawCount === 0) {
    return { state: 'empty', count: 0, credits: [], urgency: 'none' }
  }

  const rawCredits = Array.isArray(info.credits) ? info.credits : []
  const valid: Array<{ expiresAt: string; expiryMs: number | null }> = []
  for (const credit of rawCredits) {
    const expiresAt = typeof credit?.expires_at === 'string' ? credit.expires_at.trim() : ''
    if (!expiresAt) continue
    const parsedMs = new Date(expiresAt).getTime()
    if (!Number.isNaN(parsedMs)) {
      if (parsedMs <= nowMs) continue
      valid.push({ expiresAt, expiryMs: parsedMs })
    } else {
      valid.push({ expiresAt, expiryMs: null })
    }
  }

  valid.sort((a, b) => {
    if (a.expiryMs !== null && b.expiryMs !== null) {
      if (a.expiryMs !== b.expiryMs) return a.expiryMs - b.expiryMs
    } else if (a.expiryMs !== null) {
      return -1
    } else if (b.expiryMs !== null) {
      return 1
    }
    return a.expiresAt.localeCompare(b.expiresAt)
  })

  const count = Math.min(rawCount, valid.length)
  if (count <= 0) {
    return { state: 'unknown', count: 0, credits: [], urgency: 'none' }
  }

  const credits: NormalizedResetCreditItem[] = valid.map((item) => ({
    expiresAt: item.expiresAt,
    remainingMs: item.expiryMs !== null ? Math.max(0, item.expiryMs - nowMs) : null,
  }))

  const earliestMs = credits[0]?.remainingMs ?? null
  let urgency: ResetCreditUrgency = 'normal'
  if (earliestMs !== null) {
    if (earliestMs <= HOUR_MS) {
      urgency = 'urgent'
    } else if (earliestMs <= DAY_MS) {
      urgency = 'soon'
    }
  }

  return {
    state: 'available',
    count,
    credits,
    urgency,
  }
}

export function formatResetCreditDuration(
  remainingMs: number | null,
  t: (key: string, params?: Record<string, unknown>) => string,
): string {
  if (remainingMs === null || !Number.isFinite(remainingMs)) return ''
  if (remainingMs < MINUTE_MS) {
    return t('dashboard.accountWindowQuota.resetCredits.durationSoon')
  }
  const totalMinutes = Math.floor(remainingMs / MINUTE_MS)
  const days = Math.floor(totalMinutes / (24 * 60))
  const hours = Math.floor((totalMinutes % (24 * 60)) / 60)
  const minutes = totalMinutes % 60

  if (days > 0) {
    if (hours > 0) {
      return t('dashboard.accountWindowQuota.resetCredits.durationDaysHours', { days, hours })
    }
    return t('dashboard.accountWindowQuota.resetCredits.durationDays', { days })
  }
  if (hours > 0) {
    if (minutes > 0) {
      return t('dashboard.accountWindowQuota.resetCredits.durationHoursMinutes', { hours, minutes })
    }
    return t('dashboard.accountWindowQuota.resetCredits.durationHours', { hours })
  }
  return t('dashboard.accountWindowQuota.resetCredits.durationMinutes', { minutes: Math.max(1, minutes) })
}

export function formatResetCreditAbsoluteTime(
  iso: string,
  style: 'short' | 'full' = 'short',
): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return iso
  const options: Intl.DateTimeFormatOptions = {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }
  if (style === 'full') {
    options.year = 'numeric'
  }
  return new Intl.DateTimeFormat(undefined, options).format(date)
}
