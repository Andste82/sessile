import { createApp } from "vue";
import { createPinia } from "pinia";
import App from "./App.vue";
import { router } from "./router";
import { setUnauthorizedHandler } from "./api/client";
import { useAuthStore } from "./stores/auth";
import "./style.css";

const app = createApp(App);
app.use(createPinia());

// A 401 from any request — a lapsed or revoked session cookie — clears the
// auth store and sends the app back to /login from wherever it happened,
// rather than every call site needing to check for this itself.
const auth = useAuthStore();
setUnauthorizedHandler(() => {
  auth.clear();
  if (router.currentRoute.value.name !== "login") {
    router.push({ name: "login", query: { redirect: router.currentRoute.value.fullPath } });
  }
});

app.use(router);

// Wait for the first navigation (and its beforeEach guard, router/index.ts)
// to resolve before mounting. Without this, App.vue's first render sees
// vue-router's START_LOCATION sentinel — meta: {} — so `route.meta.public`
// is false and it briefly mounts the authed AppShell (sidebar, /ws/events)
// for every visitor, logged in or not, until the guard's redirect lands a
// beat later.
router.isReady().then(() => {
  app.mount("#app");
});

// A no-op passthrough service worker (public/sw.js) — Chrome still requires
// one registered with a fetch handler for beforeinstallprompt to fire at
// all, even though its own install-from-menu path dropped that requirement.
// Registered unconditionally: a no-op has no observable effect for anyone
// who never installs, and this is what unlocks the in-app Install button
// (SettingsPage.vue) for everyone else.
if ("serviceWorker" in navigator) {
  navigator.serviceWorker.register("/sw.js").catch(() => {});
}
