import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api } from '@/api/client'
import { patchEntries, rowsFromDefaults, validateRows } from '@/utils/env'
import type { EnvRow } from '@/utils/env'

// The user's default environment variables (GET/PUT /api/env-defaults). Values
// live only in this store's memory while the Settings page is open; secrets are
// write-only, so a loaded secret row carries no value and an untouched one is
// saved back as "keep". Nothing here is persisted client-side.
export const useEnvDefaultsStore = defineStore('envDefaults', () => {
  const rows = ref<EnvRow[]>([])
  const loaded = ref(false)
  const loading = ref(false)
  const loadError = ref<string | null>(null)
  const saving = ref(false)
  const saveError = ref<string | null>(null)
  const saved = ref(false)

  async function load() {
    loading.value = true
    loadError.value = null
    try {
      const res = await api.getEnvDefaults()
      rows.value = rowsFromDefaults(res.env, true)
      loaded.value = true
    } catch (e) {
      loadError.value = e instanceof Error ? e.message : String(e)
    } finally {
      loading.value = false
    }
  }

  function setRows(next: EnvRow[]) {
    rows.value = next
    saved.value = false
  }

  async function save() {
    if (saving.value) return
    saved.value = false
    const problem = validateRows(rows.value)
    if (problem) {
      saveError.value = problem
      return
    }
    saving.value = true
    saveError.value = null
    try {
      const res = await api.putEnvDefaults(patchEntries(rows.value))
      rows.value = rowsFromDefaults(res.env, true)
      saved.value = true
    } catch (e) {
      // The server's message names entries only, never values.
      saveError.value = e instanceof Error ? e.message : String(e)
    } finally {
      saving.value = false
    }
  }

  // Drops every value held in memory, e.g. when leaving the page.
  function clear() {
    rows.value = []
    loaded.value = false
    saved.value = false
    saveError.value = null
  }

  return { rows, loaded, loading, loadError, saving, saveError, saved, load, setRows, save, clear }
})
