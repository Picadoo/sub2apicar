<template>
  <BaseDialog
    :show="show"
    :title="t('admin.users.platformQuota.title')"
    width="wide"
    @close="$emit('close')"
  >
    <div v-if="user" class="space-y-4">
      <div
        v-if="hasActiveSubscription"
        class="rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-700 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200"
      >
        {{ t('admin.users.platformQuota.subscriptionWarning') }}
      </div>
      <p class="text-sm text-gray-600 dark:text-gray-400">
        {{ t('admin.users.platformQuota.subtitle', { email: user.email }) }}
      </p>
      <div v-if="loading" class="py-10 text-center text-gray-500">{{ t('common.loading') }}</div>
      <div v-else class="overflow-x-auto">
        <table class="min-w-full text-sm" data-testid="platform-quota-table">
          <thead>
            <tr class="border-b border-gray-200 text-gray-700 dark:border-dark-700 dark:text-gray-300">
              <th class="px-3 py-2 text-left font-medium">{{ t('admin.users.platformQuota.columns.platform') }}</th>
              <th class="px-3 py-2 text-left font-medium">{{ t('admin.users.platformQuota.columns.daily') }}</th>
              <th class="px-3 py-2 text-left font-medium">{{ t('admin.users.platformQuota.columns.weekly') }}</th>
              <th class="px-3 py-2 text-left font-medium">{{ t('admin.users.platformQuota.columns.monthly') }}</th>
              <th class="px-3 py-2 text-left font-medium">{{ t('admin.users.platformQuota.columns.usage') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in quotas" :key="row.platform" class="border-b border-gray-100 dark:border-dark-800">
              <td class="px-3 py-2 font-mono text-gray-900 dark:text-white">{{ row.platform }}</td>
              <td class="px-3 py-2">
                <div class="flex items-center gap-1">
                  <input
                    v-model.number="row.daily_limit_usd"
                    type="number"
                    min="0"
                    step="0.01"
                    class="input w-24"
                    :placeholder="t('admin.users.platformQuota.placeholder')"
                  />
                  <button
                    type="button"
                    class="text-xs text-gray-400 hover:text-amber-500 disabled:opacity-50"
                    :disabled="!!resetting[`${row.platform}.daily`]"
                    :title="t('admin.users.platformQuota.reset.button')"
                    @click="onReset(row.platform, 'daily')"
                  >↻</button>
                </div>
              </td>
              <td class="px-3 py-2">
                <div class="flex items-center gap-1">
                  <input
                    v-model.number="row.weekly_limit_usd"
                    type="number"
                    min="0"
                    step="0.01"
                    class="input w-24"
                    :placeholder="t('admin.users.platformQuota.placeholder')"
                  />
                  <button
                    type="button"
                    class="text-xs text-gray-400 hover:text-amber-500 disabled:opacity-50"
                    :disabled="!!resetting[`${row.platform}.weekly`]"
                    :title="t('admin.users.platformQuota.reset.button')"
                    @click="onReset(row.platform, 'weekly')"
                  >↻</button>
                </div>
              </td>
              <td class="px-3 py-2">
                <div class="flex items-center gap-1">
                  <input
                    v-model.number="row.monthly_limit_usd"
                    type="number"
                    min="0"
                    step="0.01"
                    class="input w-24"
                    :placeholder="t('admin.users.platformQuota.placeholder')"
                  />
                  <button
                    type="button"
                    class="text-xs text-gray-400 hover:text-amber-500 disabled:opacity-50"
                    :disabled="!!resetting[`${row.platform}.monthly`]"
                    :title="t('admin.users.platformQuota.reset.button')"
                    @click="onReset(row.platform, 'monthly')"
                  >↻</button>
                </div>
              </td>
              <td class="px-3 py-2 text-xs text-gray-500 dark:text-gray-400">
                {{ formatUsage(row.daily_usage_usd) }} / {{ formatUsage(row.weekly_usage_usd) }} / {{ formatUsage(row.monthly_usage_usd) }}
              </td>
            </tr>
          </tbody>
        </table>
        <p class="mt-3 text-xs text-gray-500">{{ t('admin.users.platformQuota.hint') }}</p>
        <div class="mt-3">
          <button type="button" class="btn btn-secondary text-sm" @click="onClearAll">
            {{ t('admin.users.platformQuota.clearAll') }}
          </button>
        </div>
      </div>

      <!-- 窗口百分比配额（官方 5h / 7d 窗口，按官方利用率百分点分摊，默认每人 23%） -->
      <div class="border-t border-gray-200 pt-4 dark:border-dark-700">
        <div class="mb-2 flex items-center justify-between">
          <h4 class="text-sm font-semibold text-gray-900 dark:text-white">
            {{ t('admin.users.windowQuota.title') }}
          </h4>
          <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.users.windowQuota.subtitle') }}</span>
        </div>

        <!-- 各窗口总额上限（所有用户 limit 之和的上限，全局，非按用户） -->
        <div class="mb-3 flex flex-wrap items-center gap-3 rounded-lg bg-gray-50 px-3 py-2 dark:bg-dark-800">
          <span class="text-xs text-gray-600 dark:text-gray-300">{{ t('admin.users.windowQuota.ceilingLabel') }}</span>
          <div v-for="wt in WINDOW_TYPES" :key="wt" class="flex items-center gap-1">
            <span class="text-xs text-gray-500">{{ windowLabel(wt) }}</span>
            <input
              v-model.number="ceilingDraft[wt]"
              type="number"
              min="1"
              max="100"
              step="1"
              class="input w-20"
            />
            <span class="text-xs text-gray-400">%</span>
            <button
              type="button"
              class="btn btn-primary btn-sm"
              :disabled="!!ceilingSaving[wt]"
              @click="onSaveCeiling(wt)"
            >
              {{ t('admin.users.windowQuota.save') }}
            </button>
          </div>
        </div>

        <div v-if="windowLoading" class="py-6 text-center text-gray-500">{{ t('common.loading') }}</div>
        <template v-else>
          <div
            v-if="windowGroups.length === 0"
            class="rounded-lg border border-dashed border-gray-300 px-3 py-4 text-center text-xs text-gray-500 dark:border-dark-600"
          >
            {{ t('admin.users.windowQuota.empty') }}
          </div>
          <div v-else class="space-y-3">
            <div
              v-for="g in windowGroups"
              :key="g.accountId"
              class="rounded-lg border border-gray-200 p-3 dark:border-dark-700"
            >
              <div class="mb-2 text-sm font-semibold text-gray-900 dark:text-white">
                {{ t('admin.users.windowQuota.account', { id: g.accountId }) }}
              </div>
              <table class="min-w-full text-sm">
                <thead>
                  <tr class="text-xs text-gray-500">
                    <th class="px-2 py-1 text-left font-medium">{{ t('admin.users.windowQuota.columns.window') }}</th>
                    <th class="px-2 py-1 text-left font-medium">{{ t('admin.users.windowQuota.columns.used') }}</th>
                    <th class="px-2 py-1 text-left font-medium">{{ t('admin.users.windowQuota.columns.limit') }}</th>
                    <th class="px-2 py-1 text-left font-medium">{{ t('admin.users.windowQuota.columns.reset') }}</th>
                    <th class="px-2 py-1"></th>
                  </tr>
                </thead>
                <tbody>
                  <tr
                    v-for="w in g.windows"
                    :key="w.window_type"
                    class="border-t border-gray-100 dark:border-dark-800"
                  >
                    <td class="px-2 py-1.5 text-gray-700 dark:text-gray-200">{{ windowLabel(w.window_type) }}</td>
                    <td class="px-2 py-1.5 font-mono text-gray-600 dark:text-gray-300">{{ formatPct(w.used_percent) }}%</td>
                    <td class="px-2 py-1.5">
                      <div class="flex items-center gap-1">
                        <input
                          v-model.number="w.limit_percent"
                          type="number"
                          min="0"
                          max="100"
                          step="1"
                          class="input w-20"
                        />
                        <span class="text-xs text-gray-400">%</span>
                      </div>
                    </td>
                    <td class="px-2 py-1.5 text-xs text-gray-500">{{ formatReset(w.window_reset_at) }}</td>
                    <td class="px-2 py-1.5 text-right">
                      <button
                        type="button"
                        class="btn btn-primary btn-sm"
                        :disabled="!!windowSaving[`${g.accountId}.${w.window_type}`]"
                        @click="onSaveLimit(g.accountId, w)"
                      >
                        {{ t('admin.users.windowQuota.save') }}
                      </button>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>
          <p class="mt-2 text-xs text-gray-500">{{ t('admin.users.windowQuota.hint') }}</p>
        </template>
      </div>
    </div>
    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" @click="$emit('close')">
          {{ t('admin.users.platformQuota.cancel') }}
        </button>
        <button type="button" class="btn btn-primary" :disabled="submitting || loading" @click="onSave">
          {{ submitting ? t('admin.users.platformQuota.saving') : t('admin.users.platformQuota.save') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, reactive, watch, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import {
  getUserAccountWindowQuotas,
  setAccountWindowLimit,
  getAccountWindowCeilings,
  setAccountWindowCeiling,
  type AccountWindowQuotaItem,
} from '@/api/accountWindowQuota'
import type { AdminUser, PlatformQuotaItem, PlatformQuotaPlatform, PlatformQuotaWindow } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{ show: boolean; user: AdminUser | null }>()
const emit = defineEmits(['close', 'success'])

const { t } = useI18n()
const appStore = useAppStore()

const PLATFORMS: PlatformQuotaPlatform[] = ['anthropic', 'openai', 'gemini', 'antigravity']

interface QuotaRow {
  platform: PlatformQuotaPlatform
  daily_limit_usd: number | null
  weekly_limit_usd: number | null
  monthly_limit_usd: number | null
  daily_usage_usd: number
  weekly_usage_usd: number
  monthly_usage_usd: number
}

const hasActiveSubscription = computed(() =>
  props.user?.subscriptions?.some((s) => s.status === 'active') ?? false
)

const loading = ref(false)
const submitting = ref(false)
const resetting = reactive<Record<string, boolean>>({})
const quotas = ref<QuotaRow[]>([])

// ── 窗口百分比配额（5h / 7d） ──────────────────────────────────────────────
const WINDOW_TYPES = ['5h', '7d'] as const
type WindowType = (typeof WINDOW_TYPES)[number]
const WINDOW_ORDER: Record<string, number> = { '5h': 0, '7d': 1 }

const windowLoading = ref(false)
const windowRows = ref<AccountWindowQuotaItem[]>([])
const windowSaving = reactive<Record<string, boolean>>({})
const ceilingDraft = reactive<Record<WindowType, number>>({ '5h': 92, '7d': 92 })
const ceilingSaving = reactive<Record<string, boolean>>({})

interface WindowGroup {
  accountId: number
  windows: AccountWindowQuotaItem[]
}

const windowGroups = computed<WindowGroup[]>(() => {
  const byAccount = new Map<number, AccountWindowQuotaItem[]>()
  for (const w of windowRows.value) {
    const arr = byAccount.get(w.account_id) ?? []
    arr.push(w)
    byAccount.set(w.account_id, arr)
  }
  const result: WindowGroup[] = []
  for (const [accountId, arr] of byAccount) {
    arr.sort((a, b) => (WINDOW_ORDER[a.window_type] ?? 9) - (WINDOW_ORDER[b.window_type] ?? 9))
    result.push({ accountId, windows: arr })
  }
  result.sort((a, b) => a.accountId - b.accountId)
  return result
})

function windowLabel(wt: string): string {
  if (wt === '5h') return t('admin.users.windowQuota.window5h')
  if (wt === '7d') return t('admin.users.windowQuota.window7d')
  return wt
}

function formatPct(n: number): string {
  if (!Number.isFinite(n)) return '0'
  return (Math.round(n * 10) / 10).toString()
}

function formatReset(iso: string | null | undefined): string {
  if (!iso) return '-'
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

async function loadWindows() {
  if (!props.user) return
  windowLoading.value = true
  try {
    const [wq, ceil] = await Promise.all([
      getUserAccountWindowQuotas(props.user.id),
      getAccountWindowCeilings(),
    ])
    windowRows.value = (wq.windows ?? []).map((w) => ({ ...w }))
    if (ceil.enabled) {
      for (const c of ceil.ceilings) {
        if (c.window_type === '5h' || c.window_type === '7d') {
          ceilingDraft[c.window_type] = c.ceiling_percent
        }
      }
    }
  } catch {
    windowRows.value = []
  } finally {
    windowLoading.value = false
  }
}

async function onSaveLimit(accountId: number, w: AccountWindowQuotaItem) {
  if (!props.user) return
  const v = w.limit_percent
  if (typeof v !== 'number' || !Number.isFinite(v) || v < 0 || v > 100) {
    appStore.showError(t('admin.users.windowQuota.invalidLimit'))
    return
  }
  const key = `${accountId}.${w.window_type}`
  windowSaving[key] = true
  try {
    await setAccountWindowLimit({
      user_id: props.user.id,
      account_id: accountId,
      window_type: w.window_type,
      limit_percent: v,
    })
    appStore.showSuccess(t('admin.users.windowQuota.saveSuccess'))
    await loadWindows()
  } catch (e: any) {
    appStore.showError(e?.response?.data?.message || t('admin.users.windowQuota.saveFailed'))
  } finally {
    windowSaving[key] = false
  }
}

async function onSaveCeiling(wt: WindowType) {
  const v = ceilingDraft[wt]
  if (typeof v !== 'number' || !Number.isFinite(v) || v <= 0 || v > 100) {
    appStore.showError(t('admin.users.windowQuota.invalidCeiling'))
    return
  }
  ceilingSaving[wt] = true
  try {
    await setAccountWindowCeiling({ window_type: wt, ceiling_percent: v })
    appStore.showSuccess(t('admin.users.windowQuota.saveSuccess'))
  } catch (e: any) {
    appStore.showError(e?.response?.data?.message || t('admin.users.windowQuota.saveFailed'))
  } finally {
    ceilingSaving[wt] = false
  }
}

function emptyRow(p: PlatformQuotaPlatform): QuotaRow {
  return {
    platform: p,
    daily_limit_usd: null,
    weekly_limit_usd: null,
    monthly_limit_usd: null,
    daily_usage_usd: 0,
    weekly_usage_usd: 0,
    monthly_usage_usd: 0,
  }
}

function normalize(items: PlatformQuotaItem[]): QuotaRow[] {
  const byPlatform = new Map<PlatformQuotaPlatform, PlatformQuotaItem>()
  for (const it of items) byPlatform.set(it.platform, it)
  return PLATFORMS.map((p) => {
    const it = byPlatform.get(p)
    if (!it) return emptyRow(p)
    return {
      platform: p,
      daily_limit_usd: it.daily_limit_usd ?? null,
      weekly_limit_usd: it.weekly_limit_usd ?? null,
      monthly_limit_usd: it.monthly_limit_usd ?? null,
      daily_usage_usd: it.daily_usage_usd ?? 0,
      weekly_usage_usd: it.weekly_usage_usd ?? 0,
      monthly_usage_usd: it.monthly_usage_usd ?? 0,
    }
  })
}

function formatUsage(n: number): string {
  if (n == null || Number.isNaN(n)) return '-'
  return n.toFixed(2)
}

async function load() {
  if (!props.user) return
  loading.value = true
  try {
    const data = await adminAPI.users.getPlatformQuotas(props.user.id)
    quotas.value = normalize(data.platform_quotas || [])
  } catch {
    appStore.showError(t('admin.users.platformQuota.loadFailed'))
    quotas.value = PLATFORMS.map(emptyRow)
  } finally {
    loading.value = false
  }
}

watch(
  () => props.show,
  (s) => {
    if (s && props.user) {
      load()
      loadWindows()
    }
  },
)

function onClearAll() {
  // 二次确认：一键清空全部平台的 daily/weekly/monthly 限额属于高风险批量操作，
  // 误点后所有平台变为"无限额"，且本地无 undo 机制（需要逐个手动重填或取消保存）。
  const confirmed = window.confirm(t('admin.users.platformQuota.clearAllConfirm'))
  if (!confirmed) return
  for (const row of quotas.value) {
    row.daily_limit_usd = null
    row.weekly_limit_usd = null
    row.monthly_limit_usd = null
  }
}

async function onSave() {
  if (!props.user) return
  // 校验所有 input：v-model.number 在用户输入"0."等中间状态时会写回 NaN，
  // 之前的 normalizeLimit(NaN) 静默返回 null（"无限制"），把"有限额"配置悄悄改成"无限制"。
  // 这里在 save 前显式检测 NaN，提示用户修正后再提交。
  const invalid: string[] = []
  for (const row of quotas.value) {
    for (const win of ['daily', 'weekly', 'monthly'] as const) {
      const v = row[`${win}_limit_usd` as const]
      if (typeof v === 'number' && Number.isNaN(v)) {
        invalid.push(`${row.platform}.${win}`)
      }
    }
  }
  if (invalid.length > 0) {
    appStore.showError(t('admin.users.platformQuota.invalidNumber', { fields: invalid.join(', ') }))
    return
  }

  submitting.value = true
  try {
    const payload = quotas.value.map((r) => ({
      platform: r.platform,
      daily_limit_usd: normalizeLimit(r.daily_limit_usd),
      weekly_limit_usd: normalizeLimit(r.weekly_limit_usd),
      monthly_limit_usd: normalizeLimit(r.monthly_limit_usd),
    }))
    await adminAPI.users.updatePlatformQuotas(props.user.id, payload)
    appStore.showSuccess(t('admin.users.platformQuota.updateSuccess'))
    emit('success')
    emit('close')
  } catch (e: any) {
    appStore.showError(e?.response?.data?.message || t('admin.users.platformQuota.updateFailed'))
  } finally {
    submitting.value = false
  }
}

// 仅在合法输入下返回数字：null/undefined/NaN/±Inf/负数 → null（视为"无限额"）。
// 调用方负责在 NaN 路径上做单独的用户提示（见 onSave）。
function normalizeLimit(v: number | null | undefined): number | null {
  if (v === null || v === undefined) return null
  if (typeof v === 'number' && Number.isFinite(v) && v >= 0) return v
  return null
}

async function onReset(platform: PlatformQuotaPlatform, quotaWindow: PlatformQuotaWindow) {
  if (!props.user) return
  const windowLabel = t(`admin.users.platformQuota.window${quotaWindow.charAt(0).toUpperCase() + quotaWindow.slice(1)}`)
  const confirmed = window.confirm(
    t('admin.users.platformQuota.reset.confirm', { platform, window: windowLabel })
  )
  if (!confirmed) return
  const key = `${platform}.${quotaWindow}`
  resetting[key] = true
  try {
    const data = await adminAPI.users.resetPlatformQuotaWindow(props.user.id, platform, quotaWindow)
    quotas.value = normalize(data.platform_quotas || [])
    appStore.showSuccess(t('admin.users.platformQuota.reset.success', { platform, window: windowLabel }))
  } catch (e: any) {
    appStore.showError(e?.response?.data?.message || t('admin.users.platformQuota.reset.failed'))
  } finally {
    resetting[key] = false
  }
}
</script>
