<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref, watch } from 'vue'
import '@xterm/xterm/css/xterm.css'
import { useTerminal, type ConnStatus } from '@/composables/useTerminal'

const props = defineProps<{ sessionId: string }>()
const emit = defineEmits<{ (e: 'status', s: ConnStatus): void }>()

const host = ref<HTMLElement | null>(null)
const { status, open, connect, dispose } = useTerminal()

watch(status, (s) => emit('status', s))

onMounted(() => {
  if (host.value) {
    open(host.value)
    connect(props.sessionId)
  }
})

onBeforeUnmount(() => dispose())
</script>

<template>
  <div ref="host" class="h-full w-full" />
</template>
