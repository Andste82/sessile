<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { ApiRequestError } from '@/api/client'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()

const username = ref('')
const password = ref('')
const submitting = ref(false)
const error = ref<string | null>(null)
// Toggles to the register form once bootstrap is done and the admin has
// turned allowRegistration on (@/stores/auth's status). Never available
// before setup — a stray signup before there's an admin account has nowhere
// sensible to land.
const mode = ref<'login' | 'register'>('login')

const loadingStatus = ref(true)

onMounted(async () => {
  try {
    await auth.fetchStatus()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loadingStatus.value = false
  }
})

const needsSetup = computed(() => auth.status?.needsSetup ?? false)
const canRegister = computed(() => !needsSetup.value && (auth.status?.allowRegistration ?? false))
const title = computed(() => auth.status?.displayName || 'sessile')

const canSubmit = computed(() => username.value.trim().length > 0 && password.value.length > 0)

async function submit() {
  if (!canSubmit.value || submitting.value) return
  submitting.value = true
  error.value = null
  try {
    const creds = { username: username.value.trim(), password: password.value }
    if (needsSetup.value) {
      await auth.bootstrap(creds)
    } else if (mode.value === 'register') {
      await auth.register(creds)
    } else {
      await auth.login(creds)
    }
    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : '/'
    router.push(redirect)
  } catch (e) {
    error.value = e instanceof ApiRequestError ? e.message : e instanceof Error ? e.message : String(e)
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="flex h-dvh items-center justify-center bg-slate-900 px-4 text-slate-200">
    <div class="w-full max-w-sm">
      <div class="mb-6 flex items-center justify-center gap-2 text-emerald-400">
        <span class="font-mono text-2xl">&gt;_</span>
        <span class="text-xl font-semibold tracking-tight text-slate-100">{{ title }}</span>
      </div>

      <div class="rounded-lg border border-slate-700 bg-slate-800/50 p-6">
        <p v-if="loadingStatus" class="text-sm text-slate-500">Loading…</p>
        <template v-else>
          <h1 class="mb-1 text-lg font-semibold text-slate-100">
            {{ needsSetup ? 'Create the admin account' : mode === 'register' ? 'Create an account' : 'Log in' }}
          </h1>
          <p v-if="needsSetup" class="mb-5 text-sm text-slate-400">
            This server has no accounts yet. The first one you create becomes its admin.
          </p>
          <p v-else class="mb-5 text-sm text-slate-400">
            {{ mode === 'register' ? 'Choose a username and password.' : 'Sign in to continue.' }}
          </p>

          <form class="flex flex-col gap-4" @submit.prevent="submit">
            <label class="flex flex-col gap-1 text-sm">
              <span class="text-slate-400">Username</span>
              <input
                v-model="username"
                type="text"
                autofocus
                autocomplete="username"
                maxlength="64"
                class="rounded-md border border-slate-600 bg-slate-900 px-3 py-2 text-slate-100 outline-none focus:border-emerald-500"
              />
            </label>

            <label class="flex flex-col gap-1 text-sm">
              <span class="text-slate-400">Password</span>
              <input
                v-model="password"
                type="password"
                :autocomplete="needsSetup || mode === 'register' ? 'new-password' : 'current-password'"
                class="rounded-md border border-slate-600 bg-slate-900 px-3 py-2 text-slate-100 outline-none focus:border-emerald-500"
              />
            </label>

            <p v-if="error" class="text-sm text-rose-400">{{ error }}</p>

            <button
              type="submit"
              :disabled="!canSubmit || submitting"
              class="mt-1 rounded-md bg-emerald-600 px-4 py-2 text-sm font-medium text-white hover:bg-emerald-500 disabled:cursor-not-allowed disabled:opacity-50"
            >
              {{
                submitting
                  ? 'Please wait…'
                  : needsSetup
                    ? 'Create admin account'
                    : mode === 'register'
                      ? 'Create account'
                      : 'Log in'
              }}
            </button>
          </form>

          <p v-if="canRegister" class="mt-4 text-center text-sm text-slate-400">
            <template v-if="mode === 'login'">
              Need an account?
              <button
                type="button"
                class="text-emerald-400 hover:underline"
                @click="mode = 'register'; error = null"
              >
                Create one
              </button>
            </template>
            <template v-else>
              Already have an account?
              <button
                type="button"
                class="text-emerald-400 hover:underline"
                @click="mode = 'login'; error = null"
              >
                Log in
              </button>
            </template>
          </p>
        </template>
      </div>
    </div>
  </div>
</template>
