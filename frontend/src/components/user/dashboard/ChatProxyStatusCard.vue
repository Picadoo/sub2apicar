<template>
  <div v-if="enabled" class="card p-4">
    <div class="mb-3 flex items-center justify-between">
      <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
        {{ t('dashboard.chatProxy.title') }}
      </h3>
      <span
        class="rounded-full px-2 py-0.5 text-xs font-medium"
        :class="full
          ? 'bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300'
          : 'bg-green-100 text-green-700 dark:bg-green-900/40 dark:text-green-300'"
      >
        {{ active.length }} / {{ max }}
      </span>
    </div>

    <div v-if="active.length === 0" class="text-xs text-gray-500 dark:text-gray-400">
      {{ t('dashboard.chatProxy.idle') }}
    </div>
    <div v-else class="flex flex-wrap gap-1.5">
      <span
        v-for="u in active"
        :key="u.user"
        class="inline-flex items-center gap-1 rounded-md bg-gray-100 px-2 py-0.5 text-xs text-gray-700 dark:bg-dark-700 dark:text-gray-200"
      >
        <span class="h-1.5 w-1.5 rounded-full bg-green-500" />
        {{ u.user }}
        <span class="text-[10px] text-gray-400">{{ idleLabel(u.idle_seconds) }}</span>
      </span>
    </div>

    <p v-if="full" class="mt-2 text-[11px] text-amber-600 dark:text-amber-400">
      {{ t('dashboard.chatProxy.fullHint') }}
    </p>
    <p class="mt-1 text-[10px] text-gray-400">{{ t('dashboard.chatProxy.note', { max }) }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { getChatProxyStatus, type ChatProxyActiveUser } from '@/api/chatProxy'

const { t } = useI18n()

const enabled = ref(false)
const max = ref(0)
const active = ref<ChatProxyActiveUser[]>([])
let timer: ReturnType<typeof setInterval> | null = null

const full = computed(() => max.value > 0 && active.value.length >= max.value)

function idleLabel(sec: number): string {
  if (!Number.isFinite(sec) || sec < 60) return ''
  return t('dashboard.chatProxy.idleMin', { min: Math.floor(sec / 60) })
}

async function load() {
  try {
    const data = await getChatProxyStatus()
    enabled.value = !!data.enabled
    max.value = data.max ?? 0
    active.value = data.active ?? []
  } catch {
    enabled.value = false
    active.value = []
  }
}

onMounted(() => {
  load()
  timer = setInterval(load, 30000) // 每 30 秒刷新一次"谁在线"
})
onUnmounted(() => {
  if (timer) clearInterval(timer)
})
</script>
