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
            <div class="flex items-center justify-between text-xs">
              <span class="text-gray-600 dark:text-gray-300">{{ windowLabel(w.window_type) }}</span>
              <span class="font-mono text-gray-700 dark:text-gray-200">
                {{ formatPct(w.used_percent) }}% / {{ formatPct(effLimit(w)) }}%
              </span>
            </div>
            <div class="h-1.5 w-full overflow-hidden rounded-full bg-gray-200 dark:bg-dark-700">
              <div
                class="h-full rounded-full transition-all"
                :class="barClass(calcPercent(w.used_percent, effLimit(w)))"
                :style="{ width: calcPercent(w.used_percent, effLimit(w)) + '%' }"
              />
            </div>
            <!-- 借用救急池中（已用超过自己基础份额，且有人捐了） -->
            <p v-if="isBorrowing(w)" class="text-[10px] font-medium text-blue-600 dark:text-blue-400">
              {{ t('dashboard.accountWindowQuota.borrowing', { pct: formatPct(effLimit(w) - w.limit_percent) }) }}
            </p>
            <!-- 账号该窗口合计未用余量（中性提示，便于知道整车还剩多少） -->
            <p v-if="acctRemaining(w) !== null" class="text-[10px] text-gray-400">
              {{ t('dashboard.accountWindowQuota.acctRemaining', { pct: formatPct(acctRemaining(w)!) }) }}
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
                min="0"
                max="100"
                step="5"
                class="h-1.5 w-full cursor-pointer accent-blue-500"
                :value="donatePct[dKey(w)] ?? 0"
                :disabled="savingKey === dKey(w)"
                @input="onDonateInput(w, $event)"
                @change="onDonateCommit(w)"
              />
              <p class="mt-0.5 text-[10px] text-gray-500 dark:text-gray-400">
                {{ t('dashboard.accountWindowQuota.donateHint', {
                  donate: donatePct[dKey(w)] ?? 0,
                  keep: 100 - (donatePct[dKey(w)] ?? 0),
                }) }}
                <span v-if="savingKey === dKey(w)" class="ml-1 text-gray-400">…</span>
                <span v-else-if="savedKey === dKey(w)" class="ml-1 text-green-600 dark:text-green-400">✓</span>
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
} from '@/api/accountWindowQuota'

const { t } = useI18n()

const enabled = ref(false)
const windows = ref<AccountWindowQuotaItem[]>([])
// 每个 (账号, 窗口) 各自一个捐赠比例，key = `${accountId}:${window}`
const donatePct = reactive<Record<string, number>>({})
const savingKey = ref<string | null>(null)
const savedKey = ref<string | null>(null)

interface AccountGroup {
  accountId: number
  windows: AccountWindowQuotaItem[]
}

const WINDOW_ORDER: Record<string, number> = { '5h': 0, '7d': 1 }

const groups = computed<AccountGroup[]>(() => {
  const byAccount = new Map<number, AccountWindowQuotaItem[]>()
  for (const w of windows.value) {
    const arr = byAccount.get(w.account_id) ?? []
    arr.push(w)
    byAccount.set(w.account_id, arr)
  }
  const result: AccountGroup[] = []
  for (const [accountId, arr] of byAccount) {
    arr.sort((a, b) => (WINDOW_ORDER[a.window_type] ?? 9) - (WINDOW_ORDER[b.window_type] ?? 9))
    result.push({ accountId, windows: arr })
  }
  result.sort((a, b) => a.accountId - b.accountId)
  return result
})

// 捐赠状态键：账号 + 窗口
function dKey(w: AccountWindowQuotaItem): string {
  return `${w.account_id}:${w.window_type}`
}

// 有效上限：含救急池增量（后端给的 effective_limit_percent），缺失时回落到基础上限。
function effLimit(w: AccountWindowQuotaItem): number {
  const eff = w.effective_limit_percent
  return Number.isFinite(eff) && eff > 0 ? eff : w.limit_percent
}

// 是否正在借用救急池：有效上限高于基础上限且已用已超出基础份额（5h / 7d 通用）。
function isBorrowing(w: AccountWindowQuotaItem): boolean {
  return effLimit(w) > w.limit_percent + 0.01 && w.used_percent > w.limit_percent - 0.01
}

// 账号该窗口合计未用余量 = ceiling − 全员已用；无 ceiling 数据时返回 null（不显示）。
function acctRemaining(w: AccountWindowQuotaItem): number | null {
  const ceiling = w.ceiling_percent ?? 0
  if (!ceiling || ceiling <= 0) return null
  const used = w.account_used_percent ?? 0
  return Math.max(0, ceiling - used)
}

function windowLabel(wt: string): string {
  if (wt === '5h') return t('dashboard.accountWindowQuota.window5h')
  if (wt === '7d') return t('dashboard.accountWindowQuota.window7d')
  return wt
}

function calcPercent(used: number, limit: number): number {
  if (!limit || limit <= 0) return 0
  return Math.min(100, Math.max(0, Math.round((used / limit) * 100)))
}

function barClass(p: number): string {
  if (p >= 95) return 'bg-red-500'
  if (p >= 75) return 'bg-amber-500'
  return 'bg-green-500'
}

function formatPct(n: number): string {
  if (!Number.isFinite(n)) return '0'
  return (Math.round(n * 10) / 10).toString()
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

function onDonateInput(w: AccountWindowQuotaItem, ev: Event) {
  const v = Number((ev.target as HTMLInputElement).value)
  donatePct[dKey(w)] = Number.isFinite(v) ? v : 0
}

async function onDonateCommit(w: AccountWindowQuotaItem) {
  const key = dKey(w)
  const pct = donatePct[key] ?? 0
  savingKey.value = key
  savedKey.value = null
  try {
    await setAccountWindowDonate({ account_id: w.account_id, window_type: w.window_type, fraction: pct / 100 })
    savedKey.value = key
    await load()
  } catch (error) {
    console.warn('Failed to set donate fraction:', error)
  } finally {
    savingKey.value = null
  }
}

async function load() {
  try {
    const data = await getMyAccountWindowQuotas()
    enabled.value = data.enabled
    windows.value = data.windows ?? []
    for (const w of windows.value) {
      donatePct[dKey(w)] = Math.round((w.donate_fraction ?? 0) * 100)
    }
  } catch (error) {
    console.warn('Failed to load account window quotas:', error)
    enabled.value = false
    windows.value = []
  }
}

onMounted(load)
</script>
