<template>
  <div
    v-if="visible"
    class="space-y-1"
    data-testid="carpool-reset-credits"
  >
    <div class="flex flex-wrap items-center gap-1.5 text-xs">
      <span
        class="inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-[11px] font-medium tabular-nums"
        :class="badgeClass"
        :title="primaryTooltip"
        data-testid="carpool-reset-credits-summary"
      >
        <span>{{ countLabel }}</span>
        <template v-if="primaryExpiryText">
          <span class="opacity-50">·</span>
          <span>{{ primaryExpiryText }}</span>
          <span v-if="primaryShortTime" class="opacity-75">({{ primaryShortTime }})</span>
        </template>
      </span>

      <button
        v-if="extraCount > 0"
        type="button"
        data-testid="carpool-reset-credits-toggle"
        class="inline-flex items-center rounded-md border border-gray-200 bg-gray-100 px-1.5 py-0.5 text-[11px] font-medium text-gray-600 transition-colors hover:bg-gray-200 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-300 dark:hover:bg-dark-700"
        :aria-expanded="expanded"
        :aria-label="toggleLabel"
        :title="allExpirationsTooltip"
        @click="expanded = !expanded"
      >
        +{{ extraCount }}
      </button>
    </div>

    <div
      v-if="expanded && normalized.credits.length > 1"
      data-testid="carpool-reset-credits-details"
      class="inline-grid max-w-full gap-0.5 rounded-md border border-gray-200 bg-white px-2 py-1.5 text-[11px] text-gray-600 shadow-sm dark:border-dark-700 dark:bg-dark-900 dark:text-gray-300"
    >
      <div
        v-for="(credit, index) in normalized.credits"
        :key="`${credit.expiresAt}-${index}`"
        class="flex flex-wrap items-center gap-1.5 tabular-nums"
        :title="t('dashboard.accountWindowQuota.resetCredits.expiresAtTooltip', { time: formatResetCreditAbsoluteTime(credit.expiresAt, 'full') })"
      >
        <span class="font-medium text-gray-500 dark:text-gray-400">
          {{ t('dashboard.accountWindowQuota.resetCredits.cardIndex', { index: index + 1 }) }}
        </span>
        <span>{{ itemExpiryText(credit.remainingMs) }}</span>
        <span class="text-gray-400 dark:text-gray-500">
          ({{ formatResetCreditAbsoluteTime(credit.expiresAt, 'short') }})
        </span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AccountResetCreditsInfo } from '@/api/accountWindowQuota'
import {
  formatResetCreditAbsoluteTime,
  formatResetCreditDuration,
  normalizeAccountResetCredits,
} from '@/utils/resetCreditDisplay'

const props = withDefaults(
  defineProps<{
    resetCredits?: AccountResetCreditsInfo | null
    showUnknown?: boolean
  }>(),
  {
    resetCredits: null,
    showUnknown: true,
  },
)

const { t } = useI18n()
const expanded = ref(false)

const normalized = computed(() => normalizeAccountResetCredits(props.resetCredits))

const visible = computed(() => normalized.value.state !== 'unknown' || props.showUnknown)

const countLabel = computed(() => {
  if (normalized.value.state === 'unknown') {
    return t('dashboard.accountWindowQuota.resetCredits.unknown')
  }
  if (normalized.value.state === 'empty') {
    return t('dashboard.accountWindowQuota.resetCredits.none')
  }
  return t('dashboard.accountWindowQuota.resetCredits.count', { count: normalized.value.count })
})

const primaryCredit = computed(() => normalized.value.credits[0] ?? null)

const primaryExpiryText = computed(() => {
  if (normalized.value.state !== 'available' || !primaryCredit.value) return ''
  const duration = formatResetCreditDuration(primaryCredit.value.remainingMs, t)
  if (!duration) return ''
  if (normalized.value.credits.length > 1) {
    return t('dashboard.accountWindowQuota.resetCredits.earliestExpiresIn', { duration })
  }
  return t('dashboard.accountWindowQuota.resetCredits.expiresIn', { duration })
})

const primaryShortTime = computed(() => {
  if (normalized.value.state !== 'available' || !primaryCredit.value) return ''
  return formatResetCreditAbsoluteTime(primaryCredit.value.expiresAt, 'short')
})

const primaryTooltip = computed(() => {
  if (normalized.value.state !== 'available' || !primaryCredit.value) return ''
  return t('dashboard.accountWindowQuota.resetCredits.expiresAtTooltip', {
    time: formatResetCreditAbsoluteTime(primaryCredit.value.expiresAt, 'full'),
  })
})

const extraCount = computed(() => Math.max(normalized.value.credits.length - 1, 0))

const allExpirationsTooltip = computed(() =>
  normalized.value.credits
    .map((item, idx) =>
      `${t('dashboard.accountWindowQuota.resetCredits.cardIndex', { index: idx + 1 })} ${formatResetCreditAbsoluteTime(item.expiresAt, 'full')}`,
    )
    .join('\n'),
)

const toggleLabel = computed(() =>
  expanded.value
    ? t('dashboard.accountWindowQuota.resetCredits.collapseMore')
    : t('dashboard.accountWindowQuota.resetCredits.expandMore', { count: extraCount.value }),
)

const badgeClass = computed(() => {
  if (normalized.value.state === 'unknown' || normalized.value.state === 'empty') {
    return 'border-gray-200 bg-gray-50 text-gray-500 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-400'
  }
  if (normalized.value.urgency === 'urgent') {
    return 'border-red-200 bg-red-50 text-red-700 dark:border-red-500/40 dark:bg-red-500/10 dark:text-red-300'
  }
  if (normalized.value.urgency === 'soon') {
    return 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-500/40 dark:bg-amber-500/10 dark:text-amber-300'
  }
  return 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-500/40 dark:bg-emerald-500/10 dark:text-emerald-300'
})

function itemExpiryText(remainingMs: number | null): string {
  const duration = formatResetCreditDuration(remainingMs, t)
  if (!duration) return ''
  return t('dashboard.accountWindowQuota.resetCredits.expiresIn', { duration })
}

watch(extraCount, (value) => {
  if (value <= 0) expanded.value = false
})
</script>
