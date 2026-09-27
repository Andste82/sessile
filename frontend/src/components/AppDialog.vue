<script setup lang="ts">
import { Dialog, DialogPanel, DialogTitle, TransitionRoot, TransitionChild } from '@headlessui/vue'

// The modal frame the agent-settings dialogs share: the same backdrop,
// transition and panel HostDialog.vue draws by hand.
withDefaults(defineProps<{ open: boolean; title: string; wide?: boolean }>(), { wide: false })
const emit = defineEmits<{ (e: 'close'): void }>()
</script>

<template>
  <TransitionRoot :show="open" as="template">
    <Dialog class="relative z-50" @close="emit('close')">
      <TransitionChild
        as="template"
        enter="duration-150 ease-out"
        enter-from="opacity-0"
        enter-to="opacity-100"
        leave="duration-100 ease-in"
        leave-from="opacity-100"
        leave-to="opacity-0"
      >
        <div class="fixed inset-0 bg-black/60" aria-hidden="true" />
      </TransitionChild>
      <div class="fixed inset-0 flex items-center justify-center overflow-y-auto p-4">
        <TransitionChild
          as="template"
          enter="duration-150 ease-out"
          enter-from="opacity-0 scale-95"
          enter-to="opacity-100 scale-100"
          leave="duration-100 ease-in"
          leave-from="opacity-100 scale-100"
          leave-to="opacity-0 scale-95"
        >
          <DialogPanel
            class="w-full rounded-xl border border-slate-700 bg-slate-800 p-6 shadow-xl"
            :class="wide ? 'max-w-xl' : 'max-w-md'"
          >
            <DialogTitle class="text-lg font-semibold text-slate-100">{{ title }}</DialogTitle>
            <div class="mt-5"><slot /></div>
          </DialogPanel>
        </TransitionChild>
      </div>
    </Dialog>
  </TransitionRoot>
</template>
