<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useAdminStore } from '@/stores/admin'
import { useAuthStore } from '@/stores/auth'
import { ApiRequestError } from '@/api/client'
import type { User } from '@/api/types'

const store = useAdminStore()
const auth = useAuthStore()

// Deleting/demoting is one click away everywhere else in this app (session
// deletion has no confirm step either), but a user account affects someone
// else's access, not just your own data — so this one gets an arm-then-click
// confirm instead of going straight through. armedDeleteId tracks which row,
// if any, is one click from actually deleting.
const armedDeleteId = ref<string | null>(null)
const rowError = ref<Record<string, string>>({})

onMounted(() => {
  void store.fetchUsers()
})

function arm(id: string) {
  armedDeleteId.value = id
  delete rowError.value[id]
}

async function confirmDelete(id: string) {
  armedDeleteId.value = null
  try {
    await store.deleteUser(id)
  } catch (e) {
    rowError.value[id] = e instanceof ApiRequestError ? e.message : e instanceof Error ? e.message : String(e)
  }
}

async function toggleAdmin(user: User) {
  delete rowError.value[user.id]
  try {
    await store.setAdmin(user.id, !user.isAdmin)
  } catch (e) {
    rowError.value[user.id] = e instanceof ApiRequestError ? e.message : e instanceof Error ? e.message : String(e)
  }
}
</script>

<template>
  <div class="flex h-full flex-col">
    <header class="border-b border-slate-800 bg-slate-900 px-4 py-4 sm:px-6">
      <h1 class="text-lg font-semibold tracking-tight">Users</h1>
    </header>

    <main class="mx-auto w-full max-w-2xl flex-1 overflow-y-auto p-4 sm:p-6">
      <section class="rounded-lg border border-slate-700 bg-slate-800/50 p-6">
        <div v-if="store.users.length > 0" class="flex flex-col divide-y divide-slate-700/60">
          <div
            v-for="user in store.users"
            :key="user.id"
            class="flex flex-col gap-2 py-3 first:pt-0 last:pb-0 sm:flex-row sm:items-center sm:justify-between"
          >
            <div class="min-w-0">
              <p class="truncate text-sm text-slate-100">
                {{ user.username }}
                <span v-if="user.id === auth.user?.id" class="text-xs text-slate-500">(you)</span>
              </p>
              <p class="text-xs text-slate-500">{{ user.isAdmin ? 'Admin' : 'Member' }}</p>
              <p v-if="rowError[user.id]" class="mt-1 text-xs text-rose-400">{{ rowError[user.id] }}</p>
            </div>

            <div class="flex shrink-0 items-center gap-2">
              <button
                type="button"
                class="rounded-md border border-slate-600 px-3 py-1.5 text-xs text-slate-300 hover:bg-slate-700"
                @click="toggleAdmin(user)"
              >
                {{ user.isAdmin ? 'Demote' : 'Promote' }}
              </button>

              <button
                v-if="armedDeleteId !== user.id"
                type="button"
                class="rounded-md border border-slate-600 px-3 py-1.5 text-xs text-slate-300 hover:border-rose-500 hover:text-rose-400"
                @click="arm(user.id)"
              >
                Delete
              </button>
              <template v-else>
                <button
                  type="button"
                  class="rounded-md bg-rose-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-rose-500"
                  @click="confirmDelete(user.id)"
                >
                  Confirm delete?
                </button>
                <button
                  type="button"
                  class="rounded-md px-3 py-1.5 text-xs text-slate-400 hover:bg-slate-700"
                  @click="armedDeleteId = null"
                >
                  Cancel
                </button>
              </template>
            </div>
          </div>
        </div>
        <p v-else-if="store.error" class="text-sm text-rose-400">{{ store.error }}</p>
        <p v-else-if="store.loading" class="text-sm text-slate-500">Loading…</p>
        <p v-else class="text-sm text-slate-500">No users.</p>
      </section>
    </main>
  </div>
</template>
