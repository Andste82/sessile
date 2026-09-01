import { onMounted, onUnmounted, ref } from 'vue'

// Not yet in TS's lib.dom.d.ts, so modeled narrowly here rather than pulled
// from a global declaration.
interface BeforeInstallPromptEvent extends Event {
  prompt(): Promise<void>
  userChoice: Promise<{ outcome: 'accepted' | 'dismissed' }>
}

// Wraps the browser's install-prompt flow so a page can offer its own
// "Install" button instead of relying on the address-bar icon. Chrome/Edge
// only — Safari and Firefox have no programmatic equivalent, so canInstall
// simply never becomes true there and the caller's button should just not
// render (the manual "Create shortcut" path in the README still works).
export function useInstallPrompt() {
  const canInstall = ref(false)
  // display-mode: standalone is true once already installed and launched as
  // an app — installing again from inside the installed app makes no sense.
  const installed = ref(window.matchMedia?.('(display-mode: standalone)').matches ?? false)
  const installing = ref(false)
  const error = ref<string | null>(null)

  let deferred: BeforeInstallPromptEvent | null = null

  function onBeforeInstallPrompt(e: Event) {
    // Suppresses the browser's own mini-infobar; the page's button drives it.
    e.preventDefault()
    deferred = e as BeforeInstallPromptEvent
    canInstall.value = true
  }

  function onAppInstalled() {
    installed.value = true
    canInstall.value = false
    deferred = null
  }

  onMounted(() => {
    window.addEventListener('beforeinstallprompt', onBeforeInstallPrompt)
    window.addEventListener('appinstalled', onAppInstalled)
  })
  onUnmounted(() => {
    window.removeEventListener('beforeinstallprompt', onBeforeInstallPrompt)
    window.removeEventListener('appinstalled', onAppInstalled)
  })

  async function install() {
    if (!deferred || installing.value) return
    installing.value = true
    error.value = null
    try {
      await deferred.prompt()
      await deferred.userChoice
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      // A used prompt can't be reused — a later beforeinstallprompt (if the
      // user dismissed it) replaces it and flips canInstall back on.
      deferred = null
      canInstall.value = false
      installing.value = false
    }
  }

  return { canInstall, installed, installing, error, install }
}
