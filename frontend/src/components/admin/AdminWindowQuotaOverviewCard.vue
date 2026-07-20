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
                {{ fmt(diagnostic.configured_sum_percent) }}% ·
                {{ t('admin.windowQuotaOverview.ceiling') }}
                {{ fmt(diagnostic.ceiling_percent) }}%
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
          <input
            v-model="memberSearch"
            type="search"
            class="input mb-2 w-full"
            :placeholder="t('admin.windowQuotaOverview.memberSearch')"
          />
          <div v-if="memberUsersLoading" class="py-4 text-center text-xs text-gray-500">
            {{ t('common.loading') }}
          </div>
          <div v-else class="max-h-56 space-y-1 overflow-y-auto">
            <label
              v-for="user in filteredMemberUsers"
              :key="user.id"
              class="flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-xs hover:bg-white dark:hover:bg-dark-700"
            >
              <input v-model="selectedUserIds" type="checkbox" :value="user.id" />
              <span class="text-gray-800 dark:text-gray-200">{{
                user.username || user.email || '#' + user.id
              }}</span>
              <span v-if="user.username" class="truncate text-gray-400">{{ user.email }}</span>
            </label>
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
            <button type="button" class="btn btn-secondary btn-sm" @click="closeMemberEditor">
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
  getAccountWindowQuotaOverview,
  setAccountWindowMembers,
  rebalanceAccountWindowQuotas,
  type AdminWindowQuotaSummary,
  type AdminWindowQuotaOverviewItem,
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
const memberSearch = ref('')
const memberUsersLoading = ref(false)
const memberSaving = ref(false)

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

function calcPercent(used: number, limit: number): number {
  if (!limit || limit <= 0) return 0
  return Math.min(100, Math.max(0, Math.round((used / limit) * 100)))
}
function barClass(p: number): string {
  if (p >= 95) return 'bg-red-500'
  if (p >= 75) return 'bg-amber-500'
  return 'bg-green-500'
}
function fmt(n: number | undefined): string {
  if (n == null || !Number.isFinite(n)) return '0'
  return (Math.round(n * 10) / 10).toString()
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
  memberSearch.value = ''
  try {
    await loadMemberUsers()
  } catch (error: any) {
    appStore.showError(
      error?.response?.data?.message || t('admin.windowQuotaOverview.loadMembersFailed'),
    )
  }
}

function closeMemberEditor() {
  memberEditorAccountId.value = null
  selectedUserIds.value = []
  memberSearch.value = ''
}

async function saveMembers(accountId: number) {
  if (selectedUserIds.value.length === 0) return
  if (
    !window.confirm(
      t('admin.windowQuotaOverview.membersConfirm', {
        id: accountId,
        count: selectedUserIds.value.length,
      }),
    )
  )
    return
  memberSaving.value = true
  try {
    await setAccountWindowMembers({
      account_id: accountId,
      user_ids: selectedUserIds.value,
    })
    closeMemberEditor()
    await load()
    appStore.showSuccess(t('admin.windowQuotaOverview.membersSaved'))
  } catch (error: any) {
    appStore.showError(
      error?.response?.data?.message || t('admin.windowQuotaOverview.membersSaveFailed'),
    )
  } finally {
    memberSaving.value = false
  }
}

async function load() {
  try {
    const data = await getAccountWindowQuotaOverview()
    enabled.value = data.enabled
    rows.value = data.rows ?? []
    summaries.value = data.summaries ?? []
    sharedAccountIds.value = data.account_ids ?? []
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
  if (!it) return h('span', { class: 'text-xs text-gray-400' }, '-')
  // 0 是合法有效上限（全捐且未用的捐赠者），不能回落到基础上限。
  const effRaw = it.effective_limit_percent
  const eff =
    typeof effRaw === 'number' && Number.isFinite(effRaw) && effRaw >= 0
      ? effRaw
      : it.limit_percent
  const borrowing = eff > it.limit_percent + 0.01
  const p = calcPercent(it.used_percent, eff)
  return h('div', { class: 'flex items-center gap-2' }, [
    h(
      'div',
      {
        class: 'h-1.5 w-12 shrink-0 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-700',
      },
      [
        h('div', {
          class: ['h-full rounded-full', barClass(p)],
          style: { width: p + '%' },
        }),
      ],
    ),
    h(
      'span',
      {
        class: [
          'whitespace-nowrap font-mono text-xs',
          borrowing ? 'text-blue-600 dark:text-blue-400' : 'text-gray-600 dark:text-gray-300',
        ],
      },
      `${fmt(it.used_percent)}% / ${fmt(eff)}%`,
    ),
  ])
}

onMounted(load)
</script>
