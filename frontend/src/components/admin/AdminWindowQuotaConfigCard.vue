<template>
  <div v-if="enabled" class="card p-4">
    <div class="mb-3 flex items-center justify-between">
      <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
        {{ t('admin.windowQuotaConfig.title') }}
      </h3>
      <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.windowQuotaConfig.subtitle') }}</span>
    </div>

    <div class="flex flex-wrap items-end gap-x-6 gap-y-3">
      <!-- 车位数（共享人数） -->
      <div class="flex flex-col gap-1">
        <label class="text-xs font-medium text-gray-600 dark:text-gray-300">
          {{ t('admin.windowQuotaConfig.seatsLabel') }}
        </label>
        <div class="flex items-center gap-2">
          <input v-model.number="seats" type="number" min="1" max="100" step="1" class="input w-20" />
          <span class="whitespace-nowrap text-xs text-blue-600 dark:text-blue-400">
            {{ t('admin.windowQuotaConfig.perSeat', { pct: perSeatPct }) }}
          </span>
          <button type="button" class="btn btn-primary btn-sm" :disabled="savingSeats" @click="saveSeats">
            {{ t('admin.windowQuotaConfig.save') }}
          </button>
        </div>
      </div>

      <!-- 5h 总额上限 -->
      <div class="flex flex-col gap-1">
        <label class="text-xs font-medium text-gray-600 dark:text-gray-300">
          {{ t('admin.windowQuotaConfig.ceiling5h') }}
        </label>
        <div class="flex items-center gap-2">
          <input v-model.number="ceiling5h" type="number" min="1" max="100" step="1" class="input w-20" />
          <span class="text-xs text-gray-400">%</span>
          <button type="button" class="btn btn-primary btn-sm" :disabled="saving5h" @click="saveCeiling('5h')">
            {{ t('admin.windowQuotaConfig.save') }}
          </button>
        </div>
      </div>

      <!-- 7d 总额上限 -->
      <div class="flex flex-col gap-1">
        <label class="text-xs font-medium text-gray-600 dark:text-gray-300">
          {{ t('admin.windowQuotaConfig.ceiling7d') }}
        </label>
        <div class="flex items-center gap-2">
          <input v-model.number="ceiling7d" type="number" min="1" max="100" step="1" class="input w-20" />
          <span class="text-xs text-gray-400">%</span>
          <button type="button" class="btn btn-primary btn-sm" :disabled="saving7d" @click="saveCeiling('7d')">
            {{ t('admin.windowQuotaConfig.save') }}
          </button>
        </div>
      </div>
    </div>

    <p class="mt-3 text-[11px] leading-snug text-gray-400">
      {{ t('admin.windowQuotaConfig.note') }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import {
  getAccountWindowCeilings,
  setAccountWindowCeiling,
  setAccountWindowSeats,
} from '@/api/accountWindowQuota'

const { t } = useI18n()
const appStore = useAppStore()

const enabled = ref(false)
const seats = ref(4)
const ceiling5h = ref(92)
const ceiling7d = ref(92)
const savingSeats = ref(false)
const saving5h = ref(false)
const saving7d = ref(false)

// 人均默认上限 = 5h 上限 / 车位数
const perSeatPct = computed(() => {
  if (!seats.value || seats.value <= 0) return 0
  return Math.round((ceiling5h.value / seats.value) * 10) / 10
})

async function load() {
  try {
    const data = await getAccountWindowCeilings()
    enabled.value = data.enabled
    for (const c of data.ceilings ?? []) {
      if (c.window_type === '5h') ceiling5h.value = c.ceiling_percent
      else if (c.window_type === '7d') ceiling7d.value = c.ceiling_percent
    }
    if (typeof data.seats === 'number' && data.seats > 0) seats.value = data.seats
  } catch {
    enabled.value = false
  }
}

async function saveSeats() {
  const v = seats.value
  if (typeof v !== 'number' || !Number.isFinite(v) || v < 1 || v > 100) {
    appStore.showError(t('admin.windowQuotaConfig.invalidSeats'))
    return
  }
  savingSeats.value = true
  try {
    await setAccountWindowSeats(Math.round(v))
    appStore.showSuccess(t('admin.windowQuotaConfig.saved'))
  } catch (e: any) {
    appStore.showError(e?.response?.data?.message || t('admin.windowQuotaConfig.saveFailed'))
  } finally {
    savingSeats.value = false
  }
}

async function saveCeiling(wt: '5h' | '7d') {
  const v = wt === '5h' ? ceiling5h.value : ceiling7d.value
  if (typeof v !== 'number' || !Number.isFinite(v) || v <= 0 || v > 100) {
    appStore.showError(t('admin.windowQuotaConfig.invalidCeiling'))
    return
  }
  const flag = wt === '5h' ? saving5h : saving7d
  flag.value = true
  try {
    await setAccountWindowCeiling({ window_type: wt, ceiling_percent: v })
    appStore.showSuccess(t('admin.windowQuotaConfig.saved'))
  } catch (e: any) {
    appStore.showError(e?.response?.data?.message || t('admin.windowQuotaConfig.saveFailed'))
  } finally {
    flag.value = false
  }
}

onMounted(load)
</script>
