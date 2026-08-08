import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import { captureImeTraceFlag } from './utils/imeTrace'
import './style.css'

// Before anything renders: the launch URL is the only place ?debug=ime appears,
// and the router drops the query on the first navigation. Whatever route the
// app was opened on, this is what writes it down (issue #82).
captureImeTraceFlag(window.location.search, window.sessionStorage)

createApp(App).use(createPinia()).use(router).mount('#app')
