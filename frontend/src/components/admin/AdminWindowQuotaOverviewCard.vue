<template>
  <div v-if="enabled && groups.length > 0" class="card p-4">
    <div class="mb-3 flex items-center justify-between">
      <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
        {{ t('admin.windowQuotaOverview.title') }}
      </h3>
      <span class="text-xs text-gray-500 dark:text-gray-400">{{
        t('admin.windowQuotaOverview.subtitle')
      }}</span>
    </div>

    <div class="space-y-5">
      <div
        v-for="g in groups"
        :key="g.accountId"
        class="rounded-lg border border-gray-200 p-3 dark:border-dark-700"
      >
        <div class="mb-3 flex flex-wrap items-start justify-between gap-3">
          <div>
            <div class="text-xs font-semibold text-gray-700 dark:text-gray-300">
              {{ t('admin.windowQuotaOverview.account', { id: g.accountId }) }}
            </div>
            <div class="mt-2 flex flex-wrap gap-2">
              <div
                v-for="diagnostic in g.diagnostics"
                :key="diagnostic.window_type"
                :class="[
                  'rounded-md border px-2.5 py-1.5 text-xs',
                  diagnostic.overallocated
                    ? 'border-red-300 bg-red-50 text-red-700 dark:border-red-500/40 dark:bg-red-500/10 dark:text-red-300'
                    : 'border-gray-200 bg-gray-50 text-gray-600 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-300',
                ]"
              >
                <span class="font-semibold">{{ windowLabel(diagnostic.window_type) }}</span>
                · {{ t('admin.windowQuotaOverview.configuredSum') }}
                {{ fmtPct(diagnostic.configured_sum_percent) }} ·
                {{ t('admin.windowQuotaOverview.ceiling') }}
                {{ fmtPct(diagnostic.ceiling_percent) }} ·
                {{ t('admin.windowQuotaOverview.official') }}
                {{ fmtPct(diagnostic.used_sum_percent) }} ·
                {{ t('admin.windowQuotaOverview.attributedSum') }}
                {{ fmtPct(diagnostic.attributed_sum_percent) }} ·
                {{ t('admin.windowQuotaOverview.unattributed') }}
                {{ fmtPct(diagnostic.unattributed_percent) }}
                <span v-if="diagnostic.overallocated" class="ml-1 font-semibold">
                  {{ t('admin.windowQuotaOverview.overallocated') }}
                </span>
              </div>
            </div>
            <p
              v-if="g.diagnostics.some((item) => item.overallocated)"
              class="mt-2 text-xs font-medium text-red-600 dark:text-red-400"
            >
              {{ t('admin.windowQuotaOverview.overallocatedWarning') }}
            </p>
          </div>
          <div class="flex gap-2">
            <button type="button" class="btn btn-secondary btn-sm" @click="openMemberEditor(g)">
              {{ t('admin.windowQuotaOverview.manageMembers') }}
            </button>
            <button
              type="button"
              class="btn btn-secondary btn-sm"
              :disabled="rebalancingAccountId === g.accountId"
              @click="togglePreview(g.accountId)"
            >
              {{ t('admin.windowQuotaOverview.rebalance') }}
            </button>
          </div>
        </div>

        <div
          v-if="previewAccountId === g.accountId"
          class="mb-3 rounded-lg border border-blue-200 bg-blue-50 p-3 text-xs text-blue-800 dark:border-blue-500/30 dark:bg-blue-500/10 dark:text-blue-200"
          data-testid="rebalance-preview"
        >
          <div class="font-semibold">
            {{ t('admin.windowQuotaOverview.previewTitle') }}
          </div>
          <p class="mt-1">
            {{
              t('admin.windowQuotaOverview.previewMembers', {
                count: g.users.length,
              })
            }}
          </p>
          <ul class="mt-2 list-inside list-disc space-y-1">
            <li v-for="diagnostic in g.diagnostics" :key="diagnostic.window_type">
              {{ windowLabel(diagnostic.window_type) }}:
              {{
                t('admin.windowQuotaOverview.previewValue', {
                  pct: fmt(safeEqual(diagnostic, g.users.length)),
                })
              }}
            </li>
          </ul>
          <p class="mt-2 text-blue-700 dark:text-blue-300">
            {{ t('admin.windowQuotaOverview.previewHint') }}
          </p>
          <div class="mt-3 flex gap-2">
            <button
              type="button"
              class="btn btn-primary btn-sm"
              @click="confirmRebalance(g.accountId)"
            >
              {{ t('admin.windowQuotaOverview.confirmRebalance') }}
            </button>
            <button type="button" class="btn btn-secondary btn-sm" @click="previewAccountId = null">
              {{ t('common.cancel') }}
            </button>
          </div>
        </div>

        <div
          v-if="memberEditorAccountId === g.accountId"
          class="mb-3 rounded-lg border border-gray-200 bg-gray-50 p-3 dark:border-dark-700 dark:bg-dark-800"
          data-testid="member-editor"
        >
          <div class="mb-2 flex items-center justify-between gap-3">
            <div>
              <div class="text-xs font-semibold text-gray-700 dark:text-gray-200">
                {{ t('admin.windowQuotaOverview.memberEditorTitle') }}
              </div>
              <p class="mt-0.5 text-[11px] text-gray-500 dark:text-gray-400">
                {{ t('admin.windowQuotaOverview.memberEditorHint') }}
              </p>
            </div>
            <span class="text-xs text-gray-500">{{ selectedUserIds.length }}</span>
          </div>
          <div class="mb-2 grid grid-cols-1 gap-2 sm:grid-cols-2">
            <div
              v-for="windowType in WINDOW_TYPES"
              :key="windowType"
              :class="[
                'rounded-md border px-2.5 py-1.5 text-[11px]',
                allocationOverCeiling(windowType)
                  ? 'border-red-300 bg-red-50 font-medium text-red-700 dark:border-red-500/40 dark:bg-red-500/10 dark:text-red-300'
                  : 'border-gray-200 bg-white text-gray-600 dark:border-dark-700 dark:bg-dark-900 dark:text-gray-300',
              ]"
              :data-testid="`member-allocation-${windowType}`"
            >
              {{
                t('admin.windowQuotaOverview.memberAllocation', {
                  window: windowLabel(windowType),
                  total: fmt(selectedLimitTotal(windowType)),
                  ceiling: fmt(accountCeiling(g, windowType)),
                })
              }}
            </div>
          </div>
          <p class="mb-2 text-[11px] text-gray-500 dark:text-gray-400">
            {{ t('admin.windowQuotaOverview.memberLimitHint') }}
          </p>
          <input
            v-model="memberSearch"
            type="search"
            class="input mb-2 w-full"
            :disabled="memberSaving"
            :placeholder="t('admin.windowQuotaOverview.memberSearch')"
          />
          <div v-if="memberUsersLoading" class="py-4 text-center text-xs text-gray-500">
            {{ t('common.loading') }}
          </div>
          <div v-else class="max-h-72 overflow-auto">
            <div class="min-w-[30rem] space-y-1">
              <div
                class="grid grid-cols-[auto_minmax(0,1fr)_6rem_6rem] items-center gap-2 px-2 text-[11px] font-medium text-gray-500 dark:text-gray-400"
              >
                <span></span>
                <span>{{ t('admin.windowQuotaOverview.memberColumn') }}</span>
                <span>{{ t('admin.windowQuotaOverview.member5hLimit') }}</span>
                <span>{{ t('admin.windowQuotaOverview.member7dLimit') }}</span>
              </div>
              <div
                v-for="user in filteredMemberUsers"
                :key="user.id"
                class="grid grid-cols-[auto_minmax(0,1fr)_6rem_6rem] items-center gap-2 rounded px-2 py-1.5 text-xs hover:bg-white dark:hover:bg-dark-700"
              >
                <input
                  type="checkbox"
                  :checked="isMemberSelected(user.id)"
                  :disabled="memberSaving"
                  :data-testid="`member-checkbox-${user.id}`"
                  @change="onMemberToggle(user.id, $event)"
                />
                <div class="min-w-0">
                  <div class="truncate text-gray-800 dark:text-gray-200">
                    {{ user.username || user.email || '#' + user.id }}
                  </div>
                  <div v-if="user.username" class="truncate text-[11px] text-gray-400">
                    {{ user.email }}
                  </div>
                </div>
                <div
                  v-if="isMemberSelected(user.id) && memberLimitDrafts[user.id]"
                  class="flex items-center gap-1"
                >
                  <input
                    v-model.number="memberLimitDrafts[user.id].w5h"
                    type="number"
                    min="0"
                    max="100"
                    step="0.1"
                    class="input w-20"
                    :disabled="memberSaving"
                    :data-testid="`member-limit-${user.id}-5h`"
                  />
                  <span class="text-[10px] text-gray-400">%</span>
                </div>
                <span v-else class="text-gray-300 dark:text-gray-600">—</span>
                <div
                  v-if="isMemberSelected(user.id) && memberLimitDrafts[user.id]"
                  class="flex items-center gap-1"
                >
                  <input
                    v-model.number="memberLimitDrafts[user.id].w7d"
                    type="number"
                    min="0"
                    max="100"
                    step="0.1"
                    class="input w-20"
                    :disabled="memberSaving"
                    :data-testid="`member-limit-${user.id}-7d`"
                  />
                  <span class="text-[10px] text-gray-400">%</span>
                </div>
                <span v-else class="text-gray-300 dark:text-gray-600">—</span>
              </div>
            </div>
          </div>
          <div class="mt-3 flex gap-2">
            <button
              type="button"
              class="btn btn-primary btn-sm"
              :disabled="memberSaving || selectedUserIds.length === 0"
              @click="saveMembers(g.accountId)"
            >
              {{
                memberSaving
                  ? t('admin.windowQuotaOverview.savingMembers')
                  : t('admin.windowQuotaOverview.saveMembers')
              }}
            </button>
            <button
              type="button"
              class="btn btn-secondary btn-sm"
              :disabled="memberSaving"
              @click="closeMemberEditor"
            >
              {{ t('common.cancel') }}
            </button>
          </div>
        </div>

        <div class="overflow-x-auto">
          <table class="min-w-full text-sm">
            <thead>
              <tr class="border-b border-gray-200 text-xs text-gray-500 dark:border-dark-700">
                <th class="px-3 py-2 text-left font-medium">
                  {{ t('admin.windowQuotaOverview.user') }}
                </th>
                <th class="px-3 py-2 text-left font-medium">
                  {{ t('admin.windowQuotaOverview.window5h') }}
                </th>
                <th class="px-3 py-2 text-left font-medium">
                  {{ t('admin.windowQuotaOverview.window7d') }}
                </th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="u in g.users"
                :key="u.userId"
                class="border-b border-gray-100 dark:border-dark-800"
              >
                <td class="px-3 py-2">
                  <div class="flex items-center gap-1.5">
                    <span class="text-gray-900 dark:text-white">{{
                      u.username || u.email || '#' + u.userId
                    }}</span>
                    <span
                      v-for="badge in donorBadges(u)"
                      :key="badge.window"
                      class="rounded bg-blue-100 px-1 py-0.5 text-[10px] font-medium text-blue-700 dark:bg-blue-900/40 dark:text-blue-300"
                      :title="t('admin.windowQuotaOverview.donorTip')"
                    >
                      {{
                        t('admin.windowQuotaOverview.donor', {
                          window: badge.window,
                          pct: badge.pct,
                        })
                      }}
                    </span>
                  </div>
                  <div v-if="u.username && u.email" class="text-[11px] text-gray-400">
                    {{ u.email }}
                  </div>
                </td>
                <td class="px-3 py-2"><WindowCell :item="u.w5h" /></td>
                <td class="px-3 py-2"><WindowCell :item="u.w7d" /></td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, h, type FunctionalComponent } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { AdminUser } from '@/types'
import {
  getAccountWindowCeilings,
  getAccountWindowQuotaOverview,
  setAccountWindowLimit,
  setAccountWindowMembers,
  rebalanceAccountWindowQuotas,
  type AdminWindowQuotaSummary,
  type AdminWindowQuotaOverviewItem,
  type AdminWindowQuotaOverviewResponse,
} from '@/api/accountWindowQuota'

const { t } = useI18n()
const appStore = useAppStore()

const enabled = ref(false)
const rows = ref<AdminWindowQuotaOverviewItem[]>([])
const summaries = ref<AdminWindowQuotaSummary[]>([])
const sharedAccountIds = ref<number[]>([])
const previewAccountId = ref<number | null>(null)
const rebalancingAccountId = ref<number | null>(null)
const memberEditorAccountId = ref<number | null>(null)
const memberUsers = ref<AdminUser[]>([])
const selectedUserIds = ref<number[]>([])
const memberLimitDrafts = ref<Record<number, MemberLimitDraft>>({})
const memberSearch = ref('')
const memberUsersLoading = ref(false)
const memberSaving = ref(false)
const configuredSeats = ref(4)
const configuredCeilings = ref<Record<WindowType, number>>({ '5h': 92, '7d': 92 })

const WINDOW_TYPES: WindowType[] = ['5h', '7d']
const LIMIT_EPSILON = 0.0001

type WindowType = '5h' | '7d'

interface MemberLimitDraft {
  w5h: number
  w7d: number
}

interface UserRow {
  userId: number
  username: string
  email: string
  w5h?: AdminWindowQuotaOverviewItem
  w7d?: AdminWindowQuotaOverviewItem
}
interface AccountGroup {
  accountId: number
  users: UserRow[]
  diagnostics: AdminWindowQuotaSummary[]
}

const groups = computed<AccountGroup[]>(() => {
  const byAccount = new Map<number, Map<number, UserRow>>()
  for (const r of rows.value) {
    let users = byAccount.get(r.account_id)
    if (!users) {
      users = new Map<number, UserRow>()
      byAccount.set(r.account_id, users)
    }
    let u = users.get(r.user_id)
    if (!u) {
      u = {
        userId: r.user_id,
        username: r.username || '',
        email: r.email || '',
      }
      users.set(r.user_id, u)
    }
    if (r.window_type === '5h') u.w5h = r
    else if (r.window_type === '7d') u.w7d = r
  }
  const accountIds = new Set([
    ...sharedAccountIds.value,
    ...byAccount.keys(),
    ...summaries.value.map((item) => item.account_id),
  ])
  return Array.from(accountIds)
    .sort((a, b) => a - b)
    .map((accountId) => ({
      accountId,
      users: Array.from(byAccount.get(accountId)?.values() ?? []).sort(
        (a, b) => a.userId - b.userId,
      ),
      diagnostics: summaries.value
        .filter((item) => item.account_id === accountId)
        .sort((a, b) =>
          a.window_type === '5h'
            ? -1
            : b.window_type === '5h'
              ? 1
              : a.window_type.localeCompare(b.window_type),
        ),
    }))
})

const filteredMemberUsers = computed(() => {
  const query = memberSearch.value.trim().toLowerCase()
  if (!query) return memberUsers.value
  return memberUsers.value.filter((user) =>
    `${user.id} ${user.username ?? ''} ${user.email ?? ''}`.toLowerCase().includes(query),
  )
})

const currentEditorGroup = computed(() =>
  groups.value.find((group) => group.accountId === memberEditorAccountId.value),
)

function finiteNumber(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}
function calcPercent(attributed: unknown, limit: unknown): number | null {
  const safeAttributed = finiteNumber(attributed)
  const safeLimit = finiteNumber(limit)
  if (safeAttributed === null || safeLimit === null || safeLimit <= 0) return null
  return Math.min(100, Math.max(0, Math.round((safeAttributed / safeLimit) * 100)))
}
function barClass(p: number | null): string {
  if (p === null) return 'bg-gray-400 dark:bg-gray-500'
  if (p >= 95) return 'bg-red-500'
  if (p >= 75) return 'bg-amber-500'
  return 'bg-green-500'
}
function fmt(n: unknown): string {
  const value = finiteNumber(n)
  if (value === null) return '—'
  return (Math.round(value * 10) / 10).toString()
}
function fmtPct(n: unknown): string {
  const value = fmt(n)
  return value === '—' ? value : `${value}%`
}
function windowLabel(windowType: string): string {
  if (windowType === '5h') return t('admin.windowQuotaOverview.window5h')
  if (windowType === '7d') return t('admin.windowQuotaOverview.window7d')
  return windowType
}
function safeEqual(diagnostic: AdminWindowQuotaSummary, fallbackMembers: number): number {
  const members = diagnostic.member_count > 0 ? diagnostic.member_count : fallbackMembers
  return members > 0 ? diagnostic.ceiling_percent / members : 0
}

function memberDraftKey(windowType: WindowType): keyof MemberLimitDraft {
  return windowType === '5h' ? 'w5h' : 'w7d'
}

function accountCeiling(group: AccountGroup | undefined, windowType: WindowType): number {
  const diagnostic = group?.diagnostics.find((item) => item.window_type === windowType)
  return finiteNumber(diagnostic?.ceiling_percent) ?? configuredCeilings.value[windowType]
}

function defaultMemberLimit(group: AccountGroup, windowType: WindowType): number {
  const seats = configuredSeats.value > 0 ? configuredSeats.value : Math.max(group.users.length, 1)
  return Math.round((accountCeiling(group, windowType) / seats) * 10) / 10
}

function prepareMemberLimitDrafts(group: AccountGroup) {
  const drafts: Record<number, MemberLimitDraft> = {}
  const currentUsers = new Map(group.users.map((user) => [user.userId, user]))
  for (const user of memberUsers.value) {
    const current = currentUsers.get(user.id)
    drafts[user.id] = {
      w5h: finiteNumber(current?.w5h?.limit_percent) ?? defaultMemberLimit(group, '5h'),
      w7d: finiteNumber(current?.w7d?.limit_percent) ?? defaultMemberLimit(group, '7d'),
    }
  }
  memberLimitDrafts.value = drafts
}

function isMemberSelected(userId: number): boolean {
  return selectedUserIds.value.includes(userId)
}

function onMemberToggle(userId: number, event: Event) {
  const checked = (event.target as HTMLInputElement).checked
  if (checked) {
    if (!selectedUserIds.value.includes(userId)) selectedUserIds.value.push(userId)
    return
  }
  selectedUserIds.value = selectedUserIds.value.filter((id) => id !== userId)
}

function selectedLimitTotal(windowType: WindowType): number {
  const key = memberDraftKey(windowType)
  return selectedUserIds.value.reduce((sum, userId) => {
    const value = finiteNumber(memberLimitDrafts.value[userId]?.[key])
    return sum + (value ?? 0)
  }, 0)
}

function allocationOverCeiling(windowType: WindowType): boolean {
  return selectedLimitTotal(windowType) > accountCeiling(currentEditorGroup.value, windowType) + LIMIT_EPSILON
}

// 5h / 7d 捐赠各自独立，逐窗口出徽标；此前只看 5h，7d 捐赠者不显示。
function donorBadges(u: UserRow): Array<{ window: string; pct: number }> {
  const badges: Array<{ window: string; pct: number }> = []
  for (const [window, item] of [
    ['5h', u.w5h],
    ['7d', u.w7d],
  ] as const) {
    const fraction = item?.donate_fraction ?? 0
    if (fraction > 0) badges.push({ window, pct: Math.round(fraction * 1000) / 10 })
  }
  return badges
}
function togglePreview(accountId: number) {
  previewAccountId.value = previewAccountId.value === accountId ? null : accountId
}

async function loadMemberUsers() {
  if (memberUsers.value.length > 0) return
  memberUsersLoading.value = true
  try {
    const users: AdminUser[] = []
    let page = 1
    while (true) {
      const response = await adminAPI.users.list(page, 500, {
        status: 'active',
      })
      users.push(...response.items)
      if (users.length >= response.total || response.items.length === 0) break
      page += 1
    }
    memberUsers.value = users.sort((a, b) => a.id - b.id)
  } finally {
    memberUsersLoading.value = false
  }
}

async function openMemberEditor(group: AccountGroup) {
  memberEditorAccountId.value = group.accountId
  selectedUserIds.value = group.users.map((user) => user.userId)
  memberLimitDrafts.value = {}
  memberSearch.value = ''
  try {
    await loadMemberUsers()
    prepareMemberLimitDrafts(group)
  } catch (error: any) {
    appStore.showError(
      error?.response?.data?.message || t('admin.windowQuotaOverview.loadMembersFailed'),
    )
  }
}

function closeMemberEditor() {
  memberEditorAccountId.value = null
  selectedUserIds.value = []
  memberLimitDrafts.value = {}
  memberSearch.value = ''
}

interface MemberLimitUpdate {
  userId: number
  windowType: WindowType
  target: number
  current: number
  delta: number
}

function validateMemberLimits(accountId: number): boolean {
  const group = groups.value.find((item) => item.accountId === accountId)
  for (const userId of selectedUserIds.value) {
    const draft = memberLimitDrafts.value[userId]
    for (const windowType of WINDOW_TYPES) {
      const value = finiteNumber(draft?.[memberDraftKey(windowType)])
      if (value === null || value < 0 || value > 100) {
        appStore.showError(
          t('admin.windowQuotaOverview.invalidMemberLimit', {
            user: userId,
            window: windowLabel(windowType),
          }),
        )
        return false
      }
    }
  }
  for (const windowType of WINDOW_TYPES) {
    const total = selectedLimitTotal(windowType)
    const ceiling = accountCeiling(group, windowType)
    if (total > ceiling + LIMIT_EPSILON) {
      appStore.showError(
        t('admin.windowQuotaOverview.memberCeilingExceeded', {
          window: windowLabel(windowType),
          total: fmt(total),
          ceiling: fmt(ceiling),
        }),
      )
      return false
    }
  }
  return true
}

function buildMemberLimitUpdates(
  data: AdminWindowQuotaOverviewResponse,
  accountId: number,
): MemberLimitUpdate[] {
  const currentLimits = new Map<string, number>()
  for (const row of data.rows ?? []) {
    if (row.account_id !== accountId || !WINDOW_TYPES.includes(row.window_type as WindowType)) continue
    const value = finiteNumber(row.limit_percent)
    if (value !== null) currentLimits.set(`${row.user_id}.${row.window_type}`, value)
  }

  const updates: MemberLimitUpdate[] = []
  for (const userId of selectedUserIds.value) {
    const draft = memberLimitDrafts.value[userId]
    for (const windowType of WINDOW_TYPES) {
      const target = finiteNumber(draft?.[memberDraftKey(windowType)])
      if (target === null) continue
      const current = currentLimits.get(`${userId}.${windowType}`) ?? 0
      if (Math.abs(target - current) <= LIMIT_EPSILON) continue
      updates.push({ userId, windowType, target, current, delta: target - current })
    }
  }
  return updates.sort(
    (a, b) => a.delta - b.delta || a.windowType.localeCompare(b.windowType) || a.userId - b.userId,
  )
}

function applyOverviewData(data: AdminWindowQuotaOverviewResponse) {
  enabled.value = data.enabled
  rows.value = data.rows ?? []
  summaries.value = data.summaries ?? []
  sharedAccountIds.value = data.account_ids ?? []
}

async function saveMembers(accountId: number) {
  if (selectedUserIds.value.length === 0 || !validateMemberLimits(accountId)) return
  if (
    !window.confirm(
      t('admin.windowQuotaOverview.membersConfirm', {
        id: accountId,
        count: selectedUserIds.value.length,
        total5h: fmt(selectedLimitTotal('5h')),
        total7d: fmt(selectedLimitTotal('7d')),
      }),
    )
  )
    return

  memberSaving.value = true
  let membersSynced = false
  try {
    await setAccountWindowMembers({
      account_id: accountId,
      user_ids: selectedUserIds.value,
    })
    membersSynced = true

    const syncedOverview = await getAccountWindowQuotaOverview()
    for (const update of buildMemberLimitUpdates(syncedOverview, accountId)) {
      await setAccountWindowLimit({
        user_id: update.userId,
        account_id: accountId,
        window_type: update.windowType,
        limit_percent: update.target,
      })
    }

    closeMemberEditor()
    await load()
    appStore.showSuccess(t('admin.windowQuotaOverview.membersSaved'))
  } catch (error: any) {
    closeMemberEditor()
    await load()
    appStore.showError(
      error?.response?.data?.message ||
        t(
          membersSynced
            ? 'admin.windowQuotaOverview.memberLimitsSaveFailed'
            : 'admin.windowQuotaOverview.membersSaveFailed',
        ),
    )
  } finally {
    memberSaving.value = false
  }
}

async function load() {
  try {
    const [data, config] = await Promise.all([
      getAccountWindowQuotaOverview(),
      getAccountWindowCeilings().catch(() => null),
    ])
    applyOverviewData(data)
    if (config?.enabled) {
      if (typeof config.seats === 'number' && Number.isFinite(config.seats) && config.seats > 0) {
        configuredSeats.value = config.seats
      }
      for (const item of config.ceilings ?? []) {
        if (!WINDOW_TYPES.includes(item.window_type as WindowType)) continue
        const ceiling = finiteNumber(item.ceiling_percent)
        if (ceiling !== null) configuredCeilings.value[item.window_type as WindowType] = ceiling
      }
    }
  } catch (error) {
    console.warn('Failed to load window quota overview:', error)
    enabled.value = false
    rows.value = []
    summaries.value = []
    sharedAccountIds.value = []
  }
}

async function confirmRebalance(accountId: number) {
  if (!window.confirm(t('admin.windowQuotaOverview.rebalanceConfirm', { id: accountId }))) return
  rebalancingAccountId.value = accountId
  try {
    await rebalanceAccountWindowQuotas({ account_id: accountId })
    previewAccountId.value = null
    await load()
    appStore.showSuccess(t('admin.windowQuotaOverview.rebalanceSuccess'))
  } catch (error: any) {
    appStore.showError(
      error?.response?.data?.message || t('admin.windowQuotaOverview.rebalanceFailed'),
    )
  } finally {
    rebalancingAccountId.value = null
  }
}

const WindowCell: FunctionalComponent<{
  item?: AdminWindowQuotaOverviewItem
}> = (props) => {
  const it = props.item
  if (!it) return h('span', { class: 'text-xs text-gray-400' }, '—')
  const base = finiteNumber(it.limit_percent)
  // 0 是合法有效上限；缺失或非有限数保持未知，不能回落成基础上限。
  const effRaw = finiteNumber(it.effective_limit_percent)
  const effective = effRaw !== null && effRaw >= 0 ? effRaw : null
  const attributed = finiteNumber(it.used_percent)
  const borrowing = effective !== null && base !== null && effective > base + 0.01
  const p = calcPercent(attributed, effective)
  return h('div', { class: 'space-y-1' }, [
    h(
      'div',
      {
        class: 'h-1.5 w-20 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-700',
      },
      [
        h('div', {
          class: ['h-full rounded-full', barClass(p)],
          style: { width: (p ?? 0) + '%' },
          'data-testid': 'admin-window-quota-progress',
        }),
      ],
    ),
    h(
      'div',
      {
        class: [
          'flex flex-wrap gap-x-2 gap-y-0.5 whitespace-nowrap font-mono text-[10px]',
          borrowing ? 'text-blue-600 dark:text-blue-400' : 'text-gray-600 dark:text-gray-300',
        ],
      },
      [
        h('span', `${t('admin.windowQuotaOverview.attributed')} ${fmtPct(it.used_percent)}`),
        h('span', `${t('admin.windowQuotaOverview.baseLimit')} ${fmtPct(it.limit_percent)}`),
        h('span', `${t('admin.windowQuotaOverview.effectiveLimit')} ${fmtPct(effective)}`),
        h('span', `${t('admin.windowQuotaOverview.remaining')} ${fmtPct(it.remaining_percent)}`),
      ],
    ),
  ])
}

onMounted(load)
</script>
