<template>
  <div v-if="enabled && groups.length > 0" class="card p-4">
    <div class="mb-3 flex items-center justify-between">
      <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('dashboard.accountWindowQuota.title') }}</h3>
      <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('dashboard.accountWindowQuota.subtitle') }}</span>
    </div>
    <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
      <div
        v-for="g in groups"
        :key="g.accountId"
        class="rounded-lg border border-gray-200 p-3 dark:border-dark-600"
      >
        <div class="mb-2 flex items-center justify-between">
          <span class="text-sm font-semibold text-gray-900 dark:text-white">
            {{ t('dashboard.accountWindowQuota.account', { id: g.accountId }) }}
          </span>
        </div>
        <div class="space-y-3">
          <div
            v-for="w in g.windows"
            :key="w.window_type"
            class="space-y-0.5 border-t border-gray-100 pt-2 first:border-t-0 first:pt-0 dark:border-dark-700"
          >
            <div class="text-xs font-medium text-gray-600 dark:text-gray-300">
              {{ windowLabel(w.window_type) }}
            </div>

            <div
              class="flex items-end justify-between gap-3 rounded-md bg-gray-50 px-2.5 py-2 dark:bg-dark-700/50"
              data-testid="current-window-availability"
            >
              <div>
                <div class="text-[10px] text-gray-500 dark:text-gray-400">
                  {{ t('dashboard.accountWindowQuota.availableNow') }}
                </div>
                <div class="font-mono text-xl font-semibold text-gray-900 dark:text-white">
                  {{ formatPctValue(availableNow(w)) }}
                </div>
              </div>
              <p class="max-w-[62%] text-right text-[10px] leading-snug text-gray-500 dark:text-gray-400">
                {{ hasOfficialSnapshot(w)
                  ? t('dashboard.accountWindowQuota.availableSharedHint')
                  : t('dashboard.accountWindowQuota.availableEstimateHint') }}
              </p>
            </div>

            <div
              class="grid grid-cols-2 gap-x-3 gap-y-1.5 text-[10px] text-gray-500 dark:text-gray-400"
              data-testid="window-quota-metrics"
            >
              <span>
                {{ t('dashboard.accountWindowQuota.officialUsed') }}
                <strong class="font-mono font-medium text-gray-700 dark:text-gray-200">{{ formatPctValue(w.account_used_percent) }}</strong>
              </span>
              <span>
                {{ t('dashboard.accountWindowQuota.accountCeiling') }}
                <strong class="font-mono font-medium text-gray-700 dark:text-gray-200">{{ formatPctValue(w.ceiling_percent) }}</strong>
              </span>
              <span>
                {{ t('dashboard.accountWindowQuota.siteAttributed') }}
                <strong class="font-mono font-medium text-gray-700 dark:text-gray-200">{{ formatPctValue(w.used_percent) }}</strong>
              </span>
              <span>
                {{ t('dashboard.accountWindowQuota.personalLimit') }}
                <strong class="font-mono font-medium text-gray-700 dark:text-gray-200">{{ formatPctValue(effLimit(w)) }}</strong>
                <small v-if="limitsDiffer(w)" class="ml-1 text-gray-400">
                  {{ t('dashboard.accountWindowQuota.baseShare', { pct: formatPctValue(w.limit_percent) }) }}
                </small>
              </span>
            </div>
            <div class="h-1.5 w-full overflow-hidden rounded-full bg-gray-200 dark:bg-dark-700">
              <div
                class="h-full rounded-full transition-all"
                :class="barClass(quotaProgress(w))"
                :style="{ width: (quotaProgress(w) ?? 0) + '%' }"
                data-testid="window-quota-progress"
              />
            </div>
            <!-- 借用救急池中（已用超过自己基础份额，且有人捐了） -->
            <p v-if="isBorrowing(w)" class="text-[10px] font-medium text-blue-600 dark:text-blue-400">
              {{ t('dashboard.accountWindowQuota.borrowing', { pct: formatPct(borrowingBoost(w)) }) }}
            </p>
            <p class="text-[10px] leading-snug text-gray-400" data-testid="account-attribution-breakdown">
              {{ attributionExplanation(w) }}
            </p>
            <p v-if="w.window_reset_at" class="text-[10px] text-gray-400">
              {{ t('dashboard.accountWindowQuota.resetsAt', { time: formatResetTime(w.window_reset_at) }) }}
            </p>

            <!-- 自愿救急池：捐赠滑块 + 池剩余（5h / 7d 各自独立，谁愿意给谁才给） -->
            <div class="mt-1.5 rounded-md bg-gray-50 px-2 py-1.5 dark:bg-dark-700/40">
              <div class="mb-1 flex items-center justify-between text-[11px]">
                <span class="font-medium text-gray-600 dark:text-gray-300">{{ t('dashboard.accountWindowQuota.poolTitle') }}</span>
                <span class="font-mono text-gray-500 dark:text-gray-400">
                  {{ t('dashboard.accountWindowQuota.poolRemaining', { pct: formatPct(w.pool_available_percent) }) }}
                </span>
              </div>
              <input
                type="range"
                :min="minDonatePct(w)"
                max="100"
                step="0.1"
                class="h-1.5 w-full cursor-pointer accent-blue-500"
                :value="donatePct[dKey(w)] ?? 0"
                :disabled="savingKey === dKey(w)"
                @input="onDonateInput(w, $event)"
                @change="onDonateCommit(w)"
              />
              <p class="mt-0.5 text-[10px] text-gray-500 dark:text-gray-400">
                {{ t('dashboard.accountWindowQuota.donateHint', {
                  donate: formatPct(donatePct[dKey(w)] ?? 0),
                  keep: formatPct(100 - (donatePct[dKey(w)] ?? 0)),
                }) }}
                <span v-if="savingKey === dKey(w)" class="ml-1 text-gray-400">…</span>
                <span v-else-if="savedKey === dKey(w)" class="ml-1 text-green-600 dark:text-green-400">✓</span>
              </p>
              <p
                v-if="minDonatePct(w) > 0"
                class="mt-0.5 text-[10px] font-medium text-amber-600 dark:text-amber-400"
              >
                {{ t('dashboard.accountWindowQuota.lockedDonationHint', { pct: formatPct(minDonatePct(w)) }) }}
              </p>
              <p
                class="mt-0.5 text-[10px] leading-snug"
                :class="w.window_type === '7d' ? 'font-medium text-amber-600 dark:text-amber-400' : 'text-gray-400'"
              >
                {{ w.window_type === '7d'
                  ? t('dashboard.accountWindowQuota.donateNote7d')
                  : t('dashboard.accountWindowQuota.donateNote5h') }}
              </p>
            </div>
          </div>

          <div v-if="g.members.length > 0" class="border-t border-gray-200 pt-2 dark:border-dark-600">
            <div class="mb-1.5 text-[11px] font-semibold text-gray-600 dark:text-gray-300">
              {{ t('dashboard.accountWindowQuota.membersTitle') }}
            </div>
            <div class="space-y-1.5">
              <div
                v-for="member in g.members"
                :key="member.userId"
                class="rounded-md bg-gray-50 px-2 py-1.5 text-[11px] dark:bg-dark-700/40"
              >
                <div class="mb-1 truncate font-medium text-gray-700 dark:text-gray-200">
                  {{ member.name }}
                </div>
                <div class="space-y-1 font-mono text-[10px] text-gray-500 dark:text-gray-400">
                  <div>5h: {{ memberQuota(member.w5h) }}</div>
                  <div>7d: {{ memberQuota(member.w7d) }}</div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, reactive } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  getMyAccountWindowQuotas,
  setAccountWindowDonate,
  type AccountWindowQuotaItem,
  type AdminWindowQuotaOverviewItem,
} from '@/api/accountWindowQuota'

const { t } = useI18n()

const enabled = ref(false)
const windows = ref<AccountWindowQuotaItem[]>([])
const sharedMembers = ref<AdminWindowQuotaOverviewItem[]>([])
// 每个 (账号, 窗口) 各自一个捐赠比例，key = `${accountId}:${window}`
const donatePct = reactive<Record<string, number>>({})
const savingKey = ref<string | null>(null)
const savedKey = ref<string | null>(null)

interface SharedMember {
  userId: number
  name: string
  w5h?: AdminWindowQuotaOverviewItem
  w7d?: AdminWindowQuotaOverviewItem
}

interface AccountGroup {
  accountId: number
  windows: AccountWindowQuotaItem[]
  members: SharedMember[]
}

const WINDOW_ORDER: Record<string, number> = { '5h': 0, '7d': 1 }

const groups = computed<AccountGroup[]>(() => {
  const byAccount = new Map<number, AccountWindowQuotaItem[]>()
  for (const w of windows.value) {
    const arr = byAccount.get(w.account_id) ?? []
    arr.push(w)
    byAccount.set(w.account_id, arr)
  }

  const membersByAccount = new Map<number, Map<number, SharedMember>>()
  for (const row of sharedMembers.value) {
    const accountMembers = membersByAccount.get(row.account_id) ?? new Map<number, SharedMember>()
    const member = accountMembers.get(row.user_id) ?? {
      userId: row.user_id,
      name: row.username || row.email || `#${row.user_id}`,
    }
    if (row.window_type === '5h') member.w5h = row
    if (row.window_type === '7d') member.w7d = row
    accountMembers.set(row.user_id, member)
    membersByAccount.set(row.account_id, accountMembers)
  }

  const result: AccountGroup[] = []
  for (const [accountId, arr] of byAccount) {
    arr.sort((a, b) => (WINDOW_ORDER[a.window_type] ?? 9) - (WINDOW_ORDER[b.window_type] ?? 9))
    const members = Array.from(membersByAccount.get(accountId)?.values() ?? [])
      .sort((a, b) => a.userId - b.userId)
    result.push({ accountId, windows: arr, members })
  }
  result.sort((a, b) => a.accountId - b.accountId)
  return result
})

// 捐赠状态键：账号 + 窗口
function dKey(w: AccountWindowQuotaItem): string {
  return `${w.account_id}:${w.window_type}`
}

function finiteNumber(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}

// 有效上限只展示后端明确返回的有效值；0 是合法值，缺失或非有限数显示“—”。
function effLimit(w: AccountWindowQuotaItem): number | null {
  const effective = finiteNumber(w.effective_limit_percent)
  return effective !== null && effective >= 0 ? effective : null
}

function personalRemaining(w: AccountWindowQuotaItem): number | null {
  const effective = effLimit(w)
  const attributed = finiteNumber(w.used_percent)
  if (effective === null || attributed === null || attributed < 0) return null
  return Math.max(0, effective - attributed)
}

function accountHeadroom(w: AccountWindowQuotaItem): number | null {
  const official = finiteNumber(w.account_used_percent)
  const ceiling = finiteNumber(w.ceiling_percent)
  if (official === null || ceiling === null || ceiling < 0) return null
  return Math.max(0, ceiling - official)
}

function hasOfficialSnapshot(w: AccountWindowQuotaItem): boolean {
  return accountHeadroom(w) !== null
}

// 当前真实上限同时受个人额度和账号官方安全余量约束。
// 账号余量属于全体成员及外部使用共享，不再展示误导性的个人“剩余 24%”。
function availableNow(w: AccountWindowQuotaItem): number | null {
  const personal = personalRemaining(w)
  if (personal === null) return null
  const account = accountHeadroom(w)
  return account === null ? personal : Math.min(personal, account)
}

function limitsDiffer(w: AccountWindowQuotaItem): boolean {
  const effective = effLimit(w)
  const base = finiteNumber(w.limit_percent)
  return effective !== null && base !== null && Math.abs(effective - base) > 0.01
}

function attributionExplanation(w: AccountWindowQuotaItem): string {
  // force 模式：官方用量几乎全部来自站外，成员归因恒为 0，给出区别于普通未归因的明确提示。
  if (w.force_unattributed) {
    const official = finiteNumber(w.account_used_percent)
    return official !== null && official > 0.01
      ? t('dashboard.accountWindowQuota.forceUnattributedNotice', { pct: formatPctValue(official) })
      : t('dashboard.accountWindowQuota.forceUnattributedIdle')
  }
  const unattributed = finiteNumber(w.account_unattributed_percent)
  if (unattributed !== null && unattributed > 0.01) {
    return t('dashboard.accountWindowQuota.unattributedNotice', {
      pct: formatPctValue(unattributed),
    })
  }
  if (!hasOfficialSnapshot(w)) {
    return t('dashboard.accountWindowQuota.snapshotMissing')
  }
  return t('dashboard.accountWindowQuota.attributionSynced', {
    pct: formatPctValue(w.account_attributed_percent),
  })
}

// 是否正在借用救急池：有效上限高于基础上限且归因已用已超出基础份额（5h / 7d 通用）。
function isBorrowing(w: AccountWindowQuotaItem): boolean {
  const effective = effLimit(w)
  const base = finiteNumber(w.limit_percent)
  const attributed = finiteNumber(w.used_percent)
  return (
    effective !== null &&
    base !== null &&
    attributed !== null &&
    effective > base + 0.01 &&
    attributed > base - 0.01
  )
}

function borrowingBoost(w: AccountWindowQuotaItem): number {
  const effective = effLimit(w)
  const base = finiteNumber(w.limit_percent)
  return effective !== null && base !== null ? Math.max(0, effective - base) : 0
}

function windowLabel(wt: string): string {
  if (wt === '5h') return t('dashboard.accountWindowQuota.window5h')
  if (wt === '7d') return t('dashboard.accountWindowQuota.window7d')
  return wt
}

function quotaProgress(w: AccountWindowQuotaItem): number | null {
  // 进度条只表达账号官方压力（official/ceiling）；官方快照缺失时不退化为个人口径，
  // 返回 null 显示灰色，与「--」占位文案保持语义一致，避免误导。
  const official = finiteNumber(w.account_used_percent)
  const ceiling = finiteNumber(w.ceiling_percent)
  if (official === null || ceiling === null || ceiling <= 0) return null
  return Math.min(100, Math.max(0, Math.round((official / ceiling) * 100)))
}

function barClass(p: number | null): string {
  if (p === null) return 'bg-gray-400 dark:bg-gray-500'
  if (p >= 95) return 'bg-red-500'
  if (p >= 75) return 'bg-amber-500'
  return 'bg-green-500'
}

function formatPct(n: unknown): string {
  const value = finiteNumber(n)
  if (value === null) return '--'
  return (Math.round(value * 10) / 10).toString()
}

function formatPctValue(n: unknown): string {
  const value = formatPct(n)
  return value === '--' ? value : `${value}%`
}

function memberQuota(item?: AdminWindowQuotaOverviewItem): string {
  const effective = finiteNumber(item?.effective_limit_percent)
  const base = finiteNumber(item?.limit_percent)
  const personalLimit = effective !== null && effective >= 0 ? effective : base
  return [
    `${t('dashboard.accountWindowQuota.siteAttributed')} ${formatPctValue(item?.used_percent)}`,
    `${t('dashboard.accountWindowQuota.personalLimit')} ${formatPctValue(personalLimit)}`,
  ].join(' / ')
}

function formatResetTime(iso: string | null | undefined): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString(undefined, {
    month: 'numeric',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  })
}

function minDonatePct(w: AccountWindowQuotaItem): number {
  const fraction = Number(w.minimum_donate_fraction ?? 0)
  if (!Number.isFinite(fraction)) return 0
  return Math.min(100, Math.max(0, fraction * 100))
}

function onDonateInput(w: AccountWindowQuotaItem, ev: Event) {
  const v = Number((ev.target as HTMLInputElement).value)
  const normalized = Number.isFinite(v) ? v : minDonatePct(w)
  donatePct[dKey(w)] = Math.min(100, Math.max(minDonatePct(w), normalized))
}

async function onDonateCommit(w: AccountWindowQuotaItem) {
  const key = dKey(w)
  const pct = Math.min(100, Math.max(minDonatePct(w), donatePct[key] ?? 0))
  donatePct[key] = pct
  savingKey.value = key
  savedKey.value = null
  try {
    await setAccountWindowDonate({ account_id: w.account_id, window_type: w.window_type, fraction: pct / 100 })
    savedKey.value = key
    await load()
  } catch (error) {
    console.warn('Failed to set donate fraction:', error)
    await load()
  } finally {
    savingKey.value = null
  }
}

async function load() {
  try {
    const data = await getMyAccountWindowQuotas()
    enabled.value = data.enabled
    windows.value = data.windows ?? []
    sharedMembers.value = data.members ?? []
    for (const w of windows.value) {
      // 保留 0.1% 精度，与滑块 step 一致；取整会让 52.2% 之类的值显示漂移。
      donatePct[dKey(w)] = Math.max(
        minDonatePct(w),
        Math.round((w.donate_fraction ?? 0) * 1000) / 10,
      )
    }
  } catch (error) {
    console.warn('Failed to load account window quotas:', error)
    enabled.value = false
    windows.value = []
    sharedMembers.value = []
  }
}

onMounted(load)
</script>
