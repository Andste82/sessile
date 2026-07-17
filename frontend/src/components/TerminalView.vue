<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref, watch } from 'vue'
import '@xterm/xterm/css/xterm.css'
import { useTerminal, type ConnStatus } from '@/composables/useTerminal'
import { useUiStore } from '@/stores/ui'
import KeyBar from './KeyBar.vue'

const props = defineProps<{ sessionId: string }>()
const emit = defineEmits<{ (e: 'status', s: ConnStatus): void }>()

const ui = useUiStore()
const host = ref<HTMLElement | null>(null)
const { status, mods, open, connect, dispose, toggleMod, pressSpecial } =
  useTerminal()

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
  <div class="flex h-full w-full flex-col">
    <div ref="host" class="min-h-0 flex-1" />
    <KeyBar
      v-if="ui.keyBarOpen"
      :mods="mods"
      class="shrink-0"
      @mod="toggleMod"
      @key="pressSpecial"
    />
  </div>
</template>
