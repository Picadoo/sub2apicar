<template>
  <BaseDialog
    :show="show"
    :title="t('admin.users.platformQuota.title')"
    width="wide"
    @close="requestClose"
  >
    <div v-if="user" class="space-y-5">
      <div
        v-if="hasActiveSubscription"
        class="rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-700 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200"
      >
        {{ t('admin.users.platformQuota.subscriptionWarning') }}
      </div>
      <p class="text-sm text-gray-600 dark:text-gray-400">
        {{ t('admin.users.platformQuota.subtitle', { email: user.email }) }}
      </p>
      <div
        v-if="saveNotice"
        class="rounded-lg border border-green-200 bg-green-50 px-3 py-2 text-sm text-green-700 dark:border-green-500/30 dark:bg-green-500/10 dark:text-green-300"
        data-testid="platform-quota-save-success"
      >
        {{ saveNotice }}
      </div>

      <section class="rounded-xl border border-gray-200 p-4 dark:border-dark-700">
        <h4 class="text-sm font-semibold text-gray-900 dark:text-white">
          {{ t('admin.users.platformQuota.usdSectionTitle') }}
        </h4>
        <p class="mt-1 text-xs leading-relaxed text-gray-500 dark:text-gray-400">
          {{ t('admin.users.platformQuota.usdSectionDescription') }}
        </p>

        <div v-if="loading" class="py-10 text-center text-gray-500">{{ t('common.loading') }}</div>
        <div v-else class="mt-3 overflow-x-auto">
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
                  <QuotaLimitEditor
                    :state="limitState(row.daily_limit_usd)"
                    :value="row.daily_limit_usd"
                    :testid="`${row.platform}-daily`"
                    @state-change="setLimitState(row, 'daily', $event)"
                    @value-change="row.daily_limit_usd = $event"
                  />
                  <ResetButton :busy="!!resetting[`${row.platform}.daily`]" :configured="savedConfigured.has(row.platform)" @click="onReset(row.platform, 'daily')" />
                </td>
                <td class="px-3 py-2">
                  <QuotaLimitEditor
                    :state="limitState(row.weekly_limit_usd)"
                    :value="row.weekly_limit_usd"
                    :testid="`${row.platform}-weekly`"
                    @state-change="setLimitState(row, 'weekly', $event)"
                    @value-change="row.weekly_limit_usd = $event"
                  />
                  <ResetButton :busy="!!resetting[`${row.platform}.weekly`]" :configured="savedConfigured.has(row.platform)" @click="onReset(row.platform, 'weekly')" />
                </td>
                <td class="px-3 py-2">
                  <QuotaLimitEditor
                    :state="limitState(row.monthly_limit_usd)"
                    :value="row.monthly_limit_usd"
                    :testid="`${row.platform}-monthly`"
                    @state-change="setLimitState(row, 'monthly', $event)"
                    @value-change="row.monthly_limit_usd = $event"
                  />
                  <ResetButton :busy="!!resetting[`${row.platform}.monthly`]" :configured="savedConfigured.has(row.platform)" @click="onReset(row.platform, 'monthly')" />
                </td>
                <td class="px-3 py-2 text-xs text-gray-500 dark:text-gray-400">
                  {{ formatUsage(row.daily_usage_usd) }} / {{ formatUsage(row.weekly_usage_usd) }} / {{ formatUsage(row.monthly_usage_usd) }}
                </td>
              </tr>
            </tbody>
          </table>
          <div class="mt-3 rounded-lg bg-gray-50 px-3 py-2 text-xs leading-relaxed text-gray-600 dark:bg-dark-800 dark:text-gray-300">
            <strong>{{ t('admin.users.platformQuota.states.title') }}</strong>
            {{ t('admin.users.platformQuota.states.explanation') }}
          </div>
          <div class="mt-3">
            <button type="button" class="btn btn-secondary text-sm" @click="onClearAll">
              {{ t('admin.users.platformQuota.clearAll') }}
            </button>
          </div>
        </div>
      </section>

      <section class="rounded-xl border border-gray-200 p-4 dark:border-dark-700">
        <div class="mb-2 flex items-center justify-between gap-3">
          <h4 class="text-sm font-semibold text-gray-900 dark:text-white">
            {{ t('admin.users.windowQuota.title') }}
          </h4>
          <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.users.windowQuota.subtitle') }}</span>
        </div>
        <p class="mb-3 text-xs leading-relaxed text-gray-500 dark:text-gray-400">
          {{ t('admin.users.windowQuota.officialDescription') }}
        </p>
        <p class="mb-3 rounded-lg bg-gray-50 px-3 py-2 text-[11px] leading-snug text-gray-500 dark:bg-dark-800 dark:text-gray-400">
          {{ t('admin.users.windowQuota.globalConfigHint') }}
        </p>

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
              <table class="min-w-full text-sm" data-testid="window-quota-table">
                <thead>
                  <tr class="text-xs text-gray-500">
                    <th class="px-2 py-1 text-left font-medium">{{ t('admin.users.windowQuota.columns.window') }}</th>
                    <th class="px-2 py-1 text-left font-medium">{{ t('admin.users.windowQuota.columns.attributed') }}</th>
                    <th class="px-2 py-1 text-left font-medium">{{ t('admin.users.windowQuota.columns.limit') }}</th>
                    <th class="px-2 py-1 text-left font-medium">{{ t('admin.users.windowQuota.columns.effectiveLimit') }}</th>
                    <th class="px-2 py-1 text-left font-medium">{{ t('admin.users.windowQuota.columns.remaining') }}</th>
                    <th class="px-2 py-1 text-left font-medium">{{ t('admin.users.windowQuota.columns.officialAttribution') }}</th>
                    <th class="px-2 py-1 text-left font-medium">{{ t('admin.users.windowQuota.columns.reset') }}</th>
                    <th class="px-2 py-1"></th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="w in g.windows" :key="w.window_type" class="border-t border-gray-100 dark:border-dark-800">
                    <td class="px-2 py-1.5 text-gray-700 dark:text-gray-200">{{ windowLabel(w.window_type) }}</td>
                    <td class="px-2 py-1.5 font-mono text-gray-600 dark:text-gray-300">{{ formatPctValue(w.used_percent) }}</td>
                    <td class="px-2 py-1.5">
                      <div class="flex items-center gap-1">
                        <input v-model.number="w.limit_percent" type="number" min="0" max="100" step="1" class="input w-20" />
                        <span class="text-xs text-gray-400">%</span>
                      </div>
                    </td>
                    <td class="px-2 py-1.5 font-mono text-xs text-gray-500">{{ formatPctValue(effectiveLimit(w)) }}</td>
                    <td class="px-2 py-1.5 font-mono text-xs text-gray-500">{{ formatPctValue(w.remaining_percent) }}</td>
                    <td class="px-2 py-1.5 text-[11px] text-gray-500">
                      <div>
                        {{ t('admin.users.windowQuota.official') }}
                        <span class="font-mono">{{ formatPctValue(w.account_used_percent) }}</span>
                      </div>
                      <div>
                        {{ t('admin.users.windowQuota.unattributed') }}
                        <span class="font-mono">{{ formatPctValue(w.account_unattributed_percent) }}</span>
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
      </section>
    </div>
    <template #footer>
      <div class="flex w-full items-center justify-between gap-3">
        <span v-if="hasUnsavedChanges" class="text-xs text-amber-600 dark:text-amber-400">
          {{ t('admin.users.platformQuota.unsavedIndicator') }}
        </span>
        <span v-else></span>
        <div class="flex gap-3">
          <button type="button" class="btn btn-secondary" @click="requestClose">
            {{ t('admin.users.platformQuota.cancel') }}
          </button>
          <button type="button" class="btn btn-primary" :disabled="submitting || loading" @click="onSave">
            {{ submitting ? t('admin.users.platformQuota.saving') : t('admin.users.platformQuota.save') }}
          </button>
        </div>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, reactive, watch, computed, h, nextTick, type FunctionalComponent } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import {
  getUserAccountWindowQuotas,
  setAccountWindowLimit,
  type AccountWindowQuotaItem,
} from '@/api/accountWindowQuota'
import type { AdminUser, PlatformQuotaItem, PlatformQuotaPlatform, PlatformQuotaWindow } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{ show: boolean; user: AdminUser | null }>()
const emit = defineEmits(['close', 'success'])

const { t } = useI18n()
const appStore = useAppStore()

const PLATFORMS: PlatformQuotaPlatform[] = ['anthropic', 'openai', 'gemini', 'antigravity', 'grok']
type LimitState = 'unlimited' | 'disabled' | 'limited'
type LimitKey = 'daily_limit_usd' | 'weekly_limit_usd' | 'monthly_limit_usd'

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
const platformBaseline = ref('')
const windowBaseline = ref('')
const saveNotice = ref('')

// 已保存且至少配置了一档限额的平台。只有这些平台在后端有配额记录，重置用量窗口才有对象。
const savedConfigured = ref<Set<PlatformQuotaPlatform>>(new Set())

function configuredPlatforms(items: PlatformQuotaItem[]): Set<PlatformQuotaPlatform> {
  const out = new Set<PlatformQuotaPlatform>()
  for (const it of items) {
    if (it.daily_limit_usd != null || it.weekly_limit_usd != null || it.monthly_limit_usd != null) {
      out.add(it.platform)
    }
  }
  return out
}

const WINDOW_ORDER: Record<string, number> = { '5h': 0, '7d': 1 }
const windowLoading = ref(false)
const windowRows = ref<AccountWindowQuotaItem[]>([])
const windowSaving = reactive<Record<string, boolean>>({})

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
  return Array.from(byAccount, ([accountId, windows]) => ({
    accountId,
    windows: windows.sort((a, b) => (WINDOW_ORDER[a.window_type] ?? 9) - (WINDOW_ORDER[b.window_type] ?? 9)),
  })).sort((a, b) => a.accountId - b.accountId)
})

const hasUnsavedChanges = computed(() => {
  if (loading.value || windowLoading.value) return false
  return serializePlatformQuotas() !== platformBaseline.value || serializeWindowQuotas() !== windowBaseline.value
})

function limitKey(quotaWindow: PlatformQuotaWindow): LimitKey {
  return `${quotaWindow}_limit_usd` as LimitKey
}
function limitState(value: number | null): LimitState {
  if (value === null) return 'unlimited'
  if (value === 0) return 'disabled'
  return 'limited'
}
function setLimitState(row: QuotaRow, quotaWindow: PlatformQuotaWindow, state: LimitState) {
  const key = limitKey(quotaWindow)
  if (state === 'disabled') {
    const confirmed = window.confirm(t('admin.users.platformQuota.disableConfirm', {
      platform: row.platform,
      window: t(`admin.users.platformQuota.window${quotaWindow.charAt(0).toUpperCase() + quotaWindow.slice(1)}`),
    }))
    if (!confirmed) return
    row[key] = 0
    return
  }
  row[key] = state === 'unlimited' ? null : (row[key] && row[key]! > 0 ? row[key] : 1)
}

const QuotaLimitEditor: FunctionalComponent<{
  state: LimitState
  value: number | null
  testid: string
}, {
  'state-change': (state: LimitState) => void
  'value-change': (value: number) => void
}> = (editorProps, { emit: editorEmit }) => h('div', { class: 'inline-flex flex-col gap-1 align-middle' }, [
  h('select', {
    class: 'input w-28 text-xs',
    value: editorProps.state,
    'data-testid': `${editorProps.testid}-state`,
    onChange: (event: Event) => {
      const target = event.target as HTMLSelectElement
      editorEmit('state-change', target.value as LimitState)
      void nextTick(() => { target.value = editorProps.state })
    },
  }, [
    h('option', { value: 'unlimited' }, t('admin.users.platformQuota.states.unlimited')),
    h('option', { value: 'disabled' }, t('admin.users.platformQuota.states.disabled')),
    h('option', { value: 'limited' }, t('admin.users.platformQuota.states.limited')),
  ]),
  editorProps.state === 'limited'
    ? h('input', {
        class: 'input w-28',
        type: 'number',
        min: '0.01',
        step: '0.01',
        value: editorProps.value ?? 1,
        'data-testid': `${editorProps.testid}-value`,
        onInput: (event: Event) => editorEmit('value-change', (event.target as HTMLInputElement).valueAsNumber),
      })
    : null,
])

const ResetButton: FunctionalComponent<{ busy: boolean; configured: boolean }> = (buttonProps, { emit: buttonEmit }) => h('button', {
  type: 'button',
  class: 'ml-1 text-xs text-gray-400 hover:text-amber-500 disabled:opacity-50',
  disabled: buttonProps.busy || !buttonProps.configured,
  title: t(buttonProps.configured ? 'admin.users.platformQuota.reset.button' : 'admin.users.platformQuota.reset.unavailable'),
  onClick: () => buttonEmit('click'),
}, '↻')

function emptyRow(platform: PlatformQuotaPlatform): QuotaRow {
  return {
    platform,
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
  for (const item of items) byPlatform.set(item.platform, item)
  return PLATFORMS.map((platform) => {
    const item = byPlatform.get(platform)
    if (!item) return emptyRow(platform)
    return {
      platform,
      daily_limit_usd: item.daily_limit_usd ?? null,
      weekly_limit_usd: item.weekly_limit_usd ?? null,
      monthly_limit_usd: item.monthly_limit_usd ?? null,
      daily_usage_usd: item.daily_usage_usd ?? 0,
      weekly_usage_usd: item.weekly_usage_usd ?? 0,
      monthly_usage_usd: item.monthly_usage_usd ?? 0,
    }
  })
}
function serializePlatformQuotas(): string {
  return JSON.stringify(quotas.value.map((row) => [row.platform, row.daily_limit_usd, row.weekly_limit_usd, row.monthly_limit_usd]))
}
function serializeWindowQuotas(): string {
  return JSON.stringify(windowRows.value.map((row) => [row.account_id, row.window_type, row.limit_percent]))
}
function formatUsage(n: number): string {
  if (n == null || Number.isNaN(n)) return '-'
  return n.toFixed(2)
}
function windowLabel(windowType: string): string {
  if (windowType === '5h') return t('admin.users.windowQuota.window5h')
  if (windowType === '7d') return t('admin.users.windowQuota.window7d')
  return windowType
}
function finiteNumber(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}
function formatPct(n: unknown): string {
  const value = finiteNumber(n)
  if (value === null) return '—'
  return (Math.round(value * 10) / 10).toString()
}
function formatPctValue(n: unknown): string {
  const value = formatPct(n)
  return value === '—' ? value : `${value}%`
}
function effectiveLimit(windowQuota: AccountWindowQuotaItem): number | null {
  const effective = finiteNumber(windowQuota.effective_limit_percent)
  return effective !== null && effective >= 0 ? effective : null
}
function formatReset(iso: string | null | undefined): string {
  if (!iso) return '-'
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return iso
  return date.toLocaleString(undefined, { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false })
}

async function load(preserveNotice = false) {
  if (!props.user) return
  loading.value = true
  if (!preserveNotice) saveNotice.value = ''
  try {
    const data = await adminAPI.users.getPlatformQuotas(props.user.id)
    quotas.value = normalize(data.platform_quotas || [])
    platformBaseline.value = serializePlatformQuotas()
    savedConfigured.value = configuredPlatforms(data.platform_quotas || [])
  } catch {
    appStore.showError(t('admin.users.platformQuota.loadFailed'))
    quotas.value = PLATFORMS.map(emptyRow)
    savedConfigured.value = new Set()
    platformBaseline.value = serializePlatformQuotas()
  } finally {
    loading.value = false
  }
}
async function loadWindows() {
  if (!props.user) return
  windowLoading.value = true
  try {
    const response = await getUserAccountWindowQuotas(props.user.id)
    windowRows.value = (response.windows ?? []).map((windowQuota) => ({ ...windowQuota }))
  } catch {
    windowRows.value = []
  } finally {
    windowBaseline.value = serializeWindowQuotas()
    windowLoading.value = false
  }
}

watch(
  () => props.show,
  (isOpen) => {
    if (isOpen && props.user) {
      saveNotice.value = ''
      void load()
      void loadWindows()
    }
  },
)

function requestClose() {
  if (hasUnsavedChanges.value && !window.confirm(t('admin.users.platformQuota.discardConfirm'))) return
  emit('close')
}
function onClearAll() {
  if (!window.confirm(t('admin.users.platformQuota.clearAllConfirm'))) return
  for (const row of quotas.value) {
    row.daily_limit_usd = null
    row.weekly_limit_usd = null
    row.monthly_limit_usd = null
  }
}

async function onSave() {
  if (!props.user) return
  const invalid: string[] = []
  for (const row of quotas.value) {
    for (const quotaWindow of ['daily', 'weekly', 'monthly'] as const) {
      const value = row[limitKey(quotaWindow)]
      if (typeof value === 'number' && (!Number.isFinite(value) || value < 0)) invalid.push(`${row.platform}.${quotaWindow}`)
    }
  }
  if (invalid.length > 0) {
    appStore.showError(t('admin.users.platformQuota.invalidNumber', { fields: invalid.join(', ') }))
    return
  }

  submitting.value = true
  try {
    const payload = quotas.value.map((row) => ({
      platform: row.platform,
      daily_limit_usd: row.daily_limit_usd,
      weekly_limit_usd: row.weekly_limit_usd,
      monthly_limit_usd: row.monthly_limit_usd,
    }))
    await adminAPI.users.updatePlatformQuotas(props.user.id, payload)
    await load(true)
    saveNotice.value = t('admin.users.platformQuota.updateSuccess')
    appStore.showSuccess(saveNotice.value)
    emit('success')
  } catch (error: any) {
    appStore.showError(error?.response?.data?.message || t('admin.users.platformQuota.updateFailed'))
  } finally {
    submitting.value = false
  }
}

async function onSaveLimit(accountId: number, windowQuota: AccountWindowQuotaItem) {
  if (!props.user) return
  const value = windowQuota.limit_percent
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0 || value > 100) {
    appStore.showError(t('admin.users.windowQuota.invalidLimit'))
    return
  }
  if (value === 0 && !window.confirm(t('admin.users.windowQuota.disableConfirm', {
    account: accountId,
    window: windowLabel(windowQuota.window_type),
  }))) return

  const key = `${accountId}.${windowQuota.window_type}`
  windowSaving[key] = true
  try {
    await setAccountWindowLimit({
      user_id: props.user.id,
      account_id: accountId,
      window_type: windowQuota.window_type,
      limit_percent: value,
    })
    appStore.showSuccess(t('admin.users.windowQuota.saveSuccess'))
    await loadWindows()
  } catch (error: any) {
    appStore.showError(error?.response?.data?.message || t('admin.users.windowQuota.saveFailed'))
  } finally {
    windowSaving[key] = false
  }
}

async function onReset(platform: PlatformQuotaPlatform, quotaWindow: PlatformQuotaWindow) {
  if (!props.user) return
  const translatedWindow = t(`admin.users.platformQuota.window${quotaWindow.charAt(0).toUpperCase() + quotaWindow.slice(1)}`)
  if (!window.confirm(t('admin.users.platformQuota.reset.confirm', { platform, window: translatedWindow }))) return
  const key = `${platform}.${quotaWindow}`
  resetting[key] = true
  try {
    const data = await adminAPI.users.resetPlatformQuotaWindow(props.user.id, platform, quotaWindow)
    quotas.value = normalize(data.platform_quotas || [])
    savedConfigured.value = configuredPlatforms(data.platform_quotas || [])
    platformBaseline.value = serializePlatformQuotas()
    appStore.showSuccess(t('admin.users.platformQuota.reset.success', { platform, window: translatedWindow }))
  } catch (error: any) {
    appStore.showError(error?.response?.data?.message || t('admin.users.platformQuota.reset.failed'))
  } finally {
    resetting[key] = false
  }
}
</script>
