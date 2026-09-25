<template>
  <div class="border-t border-gray-200 pt-4 dark:border-dark-600">
    <label class="input-label">
      {{ t('admin.accounts.openai.requestTimezone') }}
      <select
        :value="modelValue"
        class="input mt-2 font-normal"
        data-testid="openai-request-timezone"
        @change="emit('update:modelValue', ($event.target as HTMLSelectElement).value)"
      >
        <option value="">{{ t('admin.accounts.openai.requestTimezoneUnchanged') }}</option>
        <option v-for="timezone in options" :key="timezone" :value="timezone">{{ timezone }}</option>
      </select>
    </label>
    <p class="input-hint">{{ t('admin.accounts.openai.requestTimezoneHint') }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{ modelValue: string }>()
const emit = defineEmits<{ (event: 'update:modelValue', value: string): void }>()
const { t } = useI18n()
const timezones = [
  'America/Los_Angeles', 'America/New_York', 'America/Chicago', 'America/Denver',
  'Pacific/Honolulu', 'Asia/Tokyo', 'Asia/Seoul', 'Asia/Singapore', 'Asia/Shanghai',
  'Europe/London', 'Europe/Berlin', 'Australia/Sydney', 'UTC',
]
// Preserve valid values set through the API rather than silently selecting a default.
const options = computed(() => props.modelValue && !timezones.includes(props.modelValue)
  ? [...timezones, props.modelValue]
  : timezones)
</script>
