<template>
  <div v-if="enabled && groups.length > 0" class="card p-4">
    <div class="mb-3 flex items-center justify-between">
      <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
        {{ t('admin.windowQuotaOverview.title') }}
      </h3>
      <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.windowQuotaOverview.subtitle') }}</span>
    </div>

    <div class="space-y-4">
      <div v-for="g in groups" :key="g.accountId">
        <div class="mb-1 text-xs font-semibold text-gray-700 dark:text-gray-300">
          {{ t('admin.windowQuotaOverview.account', { id: g.accountId }) }}
        </div>
        <div class="overflow-x-auto">
          <table class="min-w-full text-sm">
            <thead>
              <tr class="border-b border-gray-200 text-xs text-gray-500 dark:border-dark-700">
                <th class="px-3 py-2 text-left font-medium">{{ t('admin.windowQuotaOverview.user') }}</th>
                <th class="px-3 py-2 text-left font-medium">{{ t('admin.windowQuotaOverview.window5h') }}</th>
                <th class="px-3 py-2 text-left font-medium">{{ t('admin.windowQuotaOverview.window7d') }}</th>
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
                    <span class="text-gray-900 dark:text-white">{{ u.username || u.email || ('#' + u.userId) }}</span>
                    <span
                      v-if="donatePctOf(u) > 0"
                      class="rounded bg-blue-100 px-1 py-0.5 text-[10px] font-medium text-blue-700 dark:bg-blue-900/40 dark:text-blue-300"
                      :title="t('admin.windowQuotaOverview.donorTip')"
                    >
                      {{ t('admin.windowQuotaOverview.donor', { pct: donatePctOf(u) }) }}
                    </span>
                  </div>
                  <div v-if="u.username && u.email" class="text-[11px] text-gray-400">{{ u.email }}</div>
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
import {
  getAccountWindowQuotaOverview,
  type AdminWindowQuotaOverviewItem,
} from '@/api/accountWindowQuota'

const { t } = useI18n()

const enabled = ref(false)
const rows = ref<AdminWindowQuotaOverviewItem[]>([])

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
      u = { userId: r.user_id, username: r.username || '', email: r.email || '' }
      users.set(r.user_id, u)
    }
    if (r.window_type === '5h') u.w5h = r
    else if (r.window_type === '7d') u.w7d = r
  }
  const result: AccountGroup[] = []
  for (const [accountId, users] of byAccount) {
    const list = Array.from(users.values()).sort((a, b) => a.userId - b.userId)
    result.push({ accountId, users: list })
  }
  result.sort((a, b) => a.accountId - b.accountId)
  return result
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

// 该用户 5h 救急池捐赠比例（百分比整数）；0 表示未捐。
function donatePctOf(u: UserRow): number {
  const f = u.w5h?.donate_fraction ?? 0
  return Math.round(f * 100)
}

// 单元格：进度条 + "已用% / 有效上限%"；借池时上限>基础会标蓝。无数据显示 "-"
const WindowCell: FunctionalComponent<{ item?: AdminWindowQuotaOverviewItem }> = (props) => {
  const it = props.item
  if (!it) return h('span', { class: 'text-xs text-gray-400' }, '-')
  const eff = it.effective_limit_percent && it.effective_limit_percent > 0 ? it.effective_limit_percent : it.limit_percent
  const borrowing = eff > it.limit_percent + 0.01
  const p = calcPercent(it.used_percent, eff)
  return h('div', { class: 'flex items-center gap-2' }, [
    h('div', { class: 'h-1.5 w-12 shrink-0 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-700' }, [
      h('div', { class: ['h-full rounded-full', barClass(p)], style: { width: p + '%' } }),
    ]),
    h(
      'span',
      {
        class: [
          'whitespace-nowrap font-mono text-xs',
          borrowing ? 'text-blue-600 dark:text-blue-400' : 'text-gray-600 dark:text-gray-300',
        ],
      },
      `${fmt(it.used_percent)}% / ${fmt(eff)}%${borrowing ? ' 🤝' : ''}`,
    ),
  ])
}

onMounted(async () => {
  try {
    const data = await getAccountWindowQuotaOverview()
    enabled.value = data.enabled
    rows.value = data.rows ?? []
  } catch (error) {
    console.warn('Failed to load window quota overview:', error)
    enabled.value = false
    rows.value = []
  }
})
</script>
