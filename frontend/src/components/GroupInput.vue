<script setup lang="ts">
import { useId } from 'vue'
import { useSessionsStore } from '@/stores/sessions'

const model = defineModel<string>({ required: true })
const store = useSessionsStore()

// A plain input with a datalist, not a select: a group is created by naming
// one, so the field has to accept a name that does not exist yet. The list
// only offers what is already in use (§4.11) — typing filters it, and an empty
// field offers the lot.
//
// The id has to be unique per instance because two of these can be mounted at
// once (the create dialog and the edit dialog), and a duplicate id would point
// both inputs at whichever list the document found first.
const listId = `group-suggestions-${useId()}`
</script>

<template>
  <div class="flex flex-col gap-1">
    <input
      v-model="model"
      type="text"
      maxlength="64"
      :list="listId"
      placeholder="No group"
      class="rounded-md border border-slate-600 bg-slate-900 px-3 py-2 text-slate-100 outline-none focus:border-emerald-500"
    />
    <datalist :id="listId">
      <option v-for="g in store.groupNames" :key="g" :value="g" />
    </datalist>
  </div>
</template>
