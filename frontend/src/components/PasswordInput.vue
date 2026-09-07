<script setup lang="ts">
import { ref } from 'vue'
import { EyeIcon, EyeSlashIcon } from '@heroicons/vue/20/solid'

withDefaults(
  defineProps<{
    autocomplete?: string
    placeholder?: string
  }>(),
  { autocomplete: 'current-password', placeholder: '' },
)

const model = defineModel<string>({ required: true })

// Per-instance and never persisted: a field that comes back revealed after the
// dialog was closed and reopened shows a password to whoever is looking at the
// screen, without anyone having asked for it this time round.
const shown = ref(false)

// tabindex="-1" on the button: the eye is a mouse affordance, and putting it in
// the tab order would sit it between the password field and the submit button
// on every form that uses this.
</script>

<template>
  <div class="relative">
    <input
      v-model="model"
      :type="shown ? 'text' : 'password'"
      :autocomplete="autocomplete"
      :placeholder="placeholder"
      class="w-full rounded-md border border-slate-600 bg-slate-900 py-2 pl-3 pr-10 text-slate-100 outline-none focus:border-emerald-500"
    />
    <button
      type="button"
      tabindex="-1"
      class="absolute inset-y-0 right-0 flex w-10 items-center justify-center rounded-r-md text-slate-400 hover:text-slate-200"
      :aria-label="shown ? 'Hide password' : 'Show password'"
      :title="shown ? 'Hide password' : 'Show password'"
      @click="shown = !shown"
    >
      <EyeSlashIcon v-if="shown" class="h-4 w-4" />
      <EyeIcon v-else class="h-4 w-4" />
    </button>
  </div>
</template>
