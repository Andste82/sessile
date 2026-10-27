<script setup lang="ts">
import { computed } from 'vue'
import { PlusIcon, TrashIcon } from '@heroicons/vue/20/solid'
import PasswordInput from './PasswordInput.vue'
import {
  emptyRow,
  overrideRow,
  renameRow,
  setRowValue,
  toggleSecret,
  type EnvRow,
} from '@/utils/env'

// Shared editor for environment variables: the New session dialog, the Edit
// session dialog and the Settings defaults section. All logic lives in
// utils/env.ts; this component only wires inputs to those transitions.
// Values live in the parent's component state and nowhere else.
const props = defineProps<{
  rows: EnvRow[]
  disabled?: boolean
  disabledNote?: string
  mode: 'create' | 'edit' | 'defaults'
}>()
const emit = defineEmits<{ (e: 'update:rows', rows: EnvRow[]): void }>()

const locked = computed(() => props.disabled === true)

function replace(key: string, next: EnvRow) {
  emit(
    'update:rows',
    props.rows.map((r) => (r.key === key ? next : r)),
  )
}

function add() {
  emit('update:rows', [...props.rows, emptyRow()])
}

// An own row is dropped outright; an inherited one is only marked, so it can
// be shown struck through and brought back with Undo.
function remove(row: EnvRow) {
  if (row.origin === 'inherited') replace(row.key, { ...row, removed: true })
  else emit('update:rows', props.rows.filter((r) => r.key !== row.key))
}

const inputClass =
  'w-full rounded-md border border-slate-600 bg-slate-900 px-3 py-2 text-sm text-slate-100 outline-none focus:border-emerald-500 disabled:opacity-60'

// Accessible names carry the variable name, so a screen reader can tell the
// rows apart. A row without a name yet is "new variable".
function rowLabel(verb: string, row: EnvRow, noun = ''): string {
  const who = row.name || 'new variable'
  return noun ? `${verb} ${noun}` + (row.name ? ` ${row.name}` : '') : `${verb} ${who}`
}

function secretPlaceholder(row: EnvRow): string {
  return row.isSet && !row.retype ? 'set (leave blank to keep)' : 'value'
}
</script>

<template>
  <div class="flex flex-col gap-2">
    <p v-if="disabledNote" class="text-xs text-slate-400">{{ disabledNote }}</p>

    <p v-if="rows.length === 0" class="text-xs text-slate-500">No variables.</p>

    <ul class="flex flex-col gap-2">
      <li
        v-for="row in rows"
        :key="row.key"
        class="flex flex-col gap-1.5 rounded-md border border-slate-700 bg-slate-900/40 p-2"
        :class="row.removed ? 'opacity-60' : ''"
      >
        <div class="flex items-center gap-2">
          <input
            :value="row.name"
            type="text"
            placeholder="NAME"
            autocomplete="off"
            spellcheck="false"
            :aria-label="rowLabel('Name of', row, 'variable')"
            :disabled="locked || row.origin === 'inherited'"
            :class="[inputClass, 'font-mono', row.removed ? 'line-through' : '']"
            @input="replace(row.key, renameRow(row, ($event.target as HTMLInputElement).value))"
          />
          <span
            v-if="row.origin === 'inherited'"
            class="shrink-0 rounded bg-slate-700 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-slate-300"
          >
            {{ row.secret ? 'inherited, set' : 'inherited' }}
          </span>
        </div>

        <div class="flex items-center gap-2">
          <div class="min-w-0 flex-1">
            <template v-if="row.origin === 'inherited'">
              <p
                class="truncate rounded-md border border-transparent px-3 py-2 font-mono text-sm text-slate-400"
                :class="row.removed ? 'line-through' : ''"
              >
                {{ row.secret ? '••••••••' : row.value }}
              </p>
            </template>
            <PasswordInput
              v-else-if="row.secret"
              :model-value="row.value"
              autocomplete="new-password"
              :placeholder="secretPlaceholder(row)"
              :aria-label="rowLabel('Value of', row)"
              :disabled="locked"
              @update:model-value="replace(row.key, setRowValue(row, $event))"
            />
            <input
              v-else
              :value="row.value"
              type="text"
              placeholder="value"
              autocomplete="off"
              spellcheck="false"
              :aria-label="rowLabel('Value of', row)"
              :disabled="locked"
              :class="[inputClass, 'font-mono']"
              @input="replace(row.key, setRowValue(row, ($event.target as HTMLInputElement).value))"
            />
          </div>

          <label
            v-if="row.origin === 'own'"
            class="flex shrink-0 items-center gap-1 text-xs text-slate-300"
          >
            <input
              type="checkbox"
              :checked="row.secret"
              :aria-label="rowLabel('Secret value for', row)"
              :disabled="locked"
              class="accent-emerald-500"
              @change="replace(row.key, toggleSecret(row))"
            />
            Secret
          </label>

          <template v-if="!locked">
            <template v-if="row.origin === 'inherited'">
              <button
                v-if="row.removed"
                type="button"
                class="shrink-0 rounded px-2 py-1 text-xs text-emerald-400 hover:bg-slate-700"
                :aria-label="rowLabel('Undo removal of', row)"
                @click="replace(row.key, { ...row, removed: false })"
              >
                Undo
              </button>
              <template v-else>
                <button
                  type="button"
                  class="shrink-0 rounded px-2 py-1 text-xs text-slate-300 hover:bg-slate-700"
                  :aria-label="rowLabel('Override', row)"
                  @click="replace(row.key, overrideRow(row))"
                >
                  Override
                </button>
                <button
                  type="button"
                  class="shrink-0 rounded px-2 py-1 text-xs text-rose-400 hover:bg-slate-700"
                  :aria-label="rowLabel('Remove', row)"
                  @click="remove(row)"
                >
                  Remove
                </button>
              </template>
            </template>
            <button
              v-else
              type="button"
              class="shrink-0 rounded p-1.5 text-slate-400 hover:bg-slate-700 hover:text-rose-400"
              :aria-label="rowLabel('Remove', row)"
              :title="rowLabel('Remove', row)"
              @click="remove(row)"
            >
              <TrashIcon class="h-4 w-4" />
            </button>
          </template>
        </div>

        <p v-if="row.retype && row.value === ''" class="text-xs text-amber-400">
          Type the value again to keep this variable.
        </p>
      </li>
    </ul>

    <button
      v-if="!locked"
      type="button"
      class="flex w-fit items-center gap-1 rounded-md border border-slate-600 px-3 py-1.5 text-xs text-slate-300 hover:bg-slate-700"
      :data-mode="mode"
      @click="add"
    >
      <PlusIcon class="h-4 w-4" /> Add variable
    </button>
  </div>
</template>
