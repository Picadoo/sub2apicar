import { describe, expect, it } from 'vitest'
import {
  formatResetCreditDuration,
  normalizeAccountResetCredits,
} from '../resetCreditDisplay'

const zhT = (key: string, params?: Record<string, unknown>) => {
  const dict: Record<string, string> = {
    'dashboard.accountWindowQuota.resetCredits.durationDaysHours': '{days}天{hours}小时后',
    'dashboard.accountWindowQuota.resetCredits.durationDays': '{days}天后',
    'dashboard.accountWindowQuota.resetCredits.durationHoursMinutes': '{hours}小时{minutes}分钟后',
    'dashboard.accountWindowQuota.resetCredits.durationHours': '{hours}小时后',
    'dashboard.accountWindowQuota.resetCredits.durationMinutes': '{minutes}分钟后',
    'dashboard.accountWindowQuota.resetCredits.durationSoon': '即将',
  }
  let text = dict[key] ?? key
  for (const [k, v] of Object.entries(params ?? {})) {
    text = text.replace(`{${k}}`, String(v))
  }
  return text
}

describe('resetCreditDisplay', () => {
  const nowMs = new Date('2026-11-26T08:00:00Z').getTime()

  it('returns unknown for missing or stale snapshots', () => {
    expect(normalizeAccountResetCredits(null, nowMs).state).toBe('unknown')
    expect(
      normalizeAccountResetCredits(
        { available_count: 1, credits: [{ expires_at: '2026-11-26T07:59:00Z' }] },
        nowMs,
      ).state,
    ).toBe('unknown')
  })

  it('returns empty for explicit zero available_count', () => {
    const res = normalizeAccountResetCredits({ available_count: 0, credits: [] }, nowMs)
    expect(res.state).toBe('empty')
    expect(res.count).toBe(0)
  })

  it('filters expired credits, sorts ascending, clamps count, and computes urgency', () => {
    const res = normalizeAccountResetCredits(
      {
        available_count: 3,
        credits: [
          { expires_at: '2026-12-01T08:00:00Z' },
          { expires_at: '2026-11-25T00:00:00Z' },
          { expires_at: '2026-11-26T12:30:00Z' },
        ],
      },
      nowMs,
    )
    expect(res.state).toBe('available')
    expect(res.count).toBe(2)
    expect(res.urgency).toBe('soon')
    expect(res.credits[0].expiresAt).toBe('2026-11-26T12:30:00Z')
    expect(res.credits[1].expiresAt).toBe('2026-12-01T08:00:00Z')

    const urgent = normalizeAccountResetCredits(
      {
        available_count: 1,
        credits: [{ expires_at: '2026-11-26T08:30:00Z' }],
      },
      nowMs,
    )
    expect(urgent.urgency).toBe('urgent')
  })

  it('formats relative durations cleanly', () => {
    expect(formatResetCreditDuration(30 * 1000, zhT)).toBe('即将')
    expect(formatResetCreditDuration(45 * 60 * 1000, zhT)).toBe('45分钟后')
    expect(formatResetCreditDuration(3 * 3600 * 1000, zhT)).toBe('3小时后')
    expect(formatResetCreditDuration((3 * 3600 + 20 * 60) * 1000, zhT)).toBe('3小时20分钟后')
    expect(formatResetCreditDuration(2 * 24 * 3600 * 1000, zhT)).toBe('2天后')
    expect(formatResetCreditDuration((2 * 24 + 5) * 3600 * 1000, zhT)).toBe('2天5小时后')
  })
})
