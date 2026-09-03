// A minimal service worker whose only job is satisfying Chrome's
// beforeinstallprompt requirement: the event still requires a registered
// fetch handler even though Chrome's own menu-based "Install app" entry no
// longer does (https://developer.chrome.com/blog/update-install-criteria).
//
// Deliberately does no caching or offline support — every request passes
// straight through to the network, unmodified, so this can never interfere
// with the app's existing Cache-Control/ETag strategy for index.html and
// the content-hashed /assets/* bundle (backend/internal/api/router.go).
self.addEventListener("fetch", (event) => {
  event.respondWith(fetch(event.request));
});
