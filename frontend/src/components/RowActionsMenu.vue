<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { EllipsisVerticalIcon } from '@heroicons/vue/20/solid'
import { nextMenuIndex, shouldDropUp } from '@/utils/menu'

// One always-visible button per row, opening the row's actions as a menu.
//
// It replaces a strip of icon buttons that were hidden behind `opacity-0` and
// revealed with `group-hover:opacity-100` — invisible on touch, where there is
// no hover, and 20 px square where PROJECT_PLAN §7 asks for 44 px. Four
// adjacent 20 px targets in a scrollable list is also the shape that produces
// stray taps while swiping, and one of the four was Delete, which cannot be
// undone or interrupted once it starts.
//
// So the button is deliberately large and always rendered, and choosing an
// action takes a second, separate tap on a full-width row.

export interface MenuItem {
  key: string
  label: string
  /** Renders in red. For actions that destroy something. */
  danger?: boolean
  disabled?: boolean
}

const props = defineProps<{
  items: MenuItem[]
  /** Accessible name for the trigger, e.g. the file it acts on. */
  label: string
}>()

const emit = defineEmits<{ (e: 'select', key: string): void }>()

const open = ref(false)
const dropUp = ref(false)
const activeIndex = ref(-1)
const trigger = ref<HTMLButtonElement | null>(null)
const menu = ref<HTMLDivElement | null>(null)

const enabledItems = computed(() => props.items.filter((i) => !i.disabled))

function close(refocus = true) {
  if (!open.value) return
  open.value = false
  activeIndex.value = -1
  if (refocus) trigger.value?.focus()
}

async function toggle() {
  if (open.value) {
    close()
    return
  }
  open.value = true
  activeIndex.value = -1
  await nextTick()
  // Decide the direction from where the trigger actually sits: a menu opened
  // near the bottom of the viewport would otherwise render off-screen, and in
  // a scrollable list that is most of the rows.
  const rect = trigger.value?.getBoundingClientRect()
  const height = menu.value?.offsetHeight ?? 0
  dropUp.value = !!rect && shouldDropUp(rect.top, rect.bottom, height, window.innerHeight)
}

function choose(item: MenuItem) {
  if (item.disabled) return
  close()
  emit('select', item.key)
}

function move(delta: number) {
  activeIndex.value = nextMenuIndex(activeIndex.value, delta, enabledItems.value.length)
}

function onKeydown(e: KeyboardEvent) {
  if (!open.value) {
    if (e.key === 'ArrowDown' || e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      void toggle()
    }
    return
  }
  switch (e.key) {
    case 'Escape':
      e.preventDefault()
      close()
      break
    case 'ArrowDown':
      e.preventDefault()
      move(1)
      break
    case 'ArrowUp':
      e.preventDefault()
      move(-1)
      break
    case 'Home':
      e.preventDefault()
      activeIndex.value = 0
      break
    case 'End':
      e.preventDefault()
      activeIndex.value = enabledItems.value.length - 1
      break
    case 'Enter':
    case ' ': {
      e.preventDefault()
      const item = enabledItems.value[activeIndex.value]
      if (item) choose(item)
      break
    }
  }
}

// Pointerdown rather than click: a click listener would fire after the button
// that opened another row's menu had already toggled it, closing it again.
function onDocumentPointerDown(e: PointerEvent) {
  const target = e.target as Node | null
  if (!target) return
  if (trigger.value?.contains(target) || menu.value?.contains(target)) return
  close(false)
}

watch(open, (isOpen) => {
  if (isOpen) {
    document.addEventListener('pointerdown', onDocumentPointerDown, true)
  } else {
    document.removeEventListener('pointerdown', onDocumentPointerDown, true)
  }
})

onBeforeUnmount(() => document.removeEventListener('pointerdown', onDocumentPointerDown, true))
</script>

<template>
  <div class="relative shrink-0">
    <button
      ref="trigger"
      type="button"
      class="flex h-11 w-11 items-center justify-center rounded text-slate-400 hover:bg-slate-800 hover:text-slate-200 focus:outline-none focus-visible:ring-1 focus-visible:ring-emerald-500"
      :aria-label="`Actions for ${label}`"
      aria-haspopup="menu"
      :aria-expanded="open"
      @click="toggle"
      @keydown="onKeydown"
    >
      <EllipsisVerticalIcon class="h-5 w-5" />
    </button>

    <div
      v-if="open"
      ref="menu"
      role="menu"
      class="absolute right-0 z-20 min-w-[12rem] overflow-hidden rounded-md border border-slate-700 bg-slate-900 py-1 shadow-lg"
      :class="dropUp ? 'bottom-full mb-1' : 'top-full mt-1'"
      @keydown="onKeydown"
    >
      <button
        v-for="item in items"
        :key="item.key"
        type="button"
        role="menuitem"
        :disabled="item.disabled"
        class="flex min-h-[2.75rem] w-full items-center px-4 text-left text-sm disabled:cursor-default disabled:opacity-40"
        :class="[
          item.danger ? 'text-rose-400 hover:bg-rose-500/10' : 'text-slate-200 hover:bg-slate-800',
          enabledItems[activeIndex]?.key === item.key ? (item.danger ? 'bg-rose-500/10' : 'bg-slate-800') : '',
        ]"
        @click="choose(item)"
      >
        {{ item.label }}
      </button>
    </div>
  </div>
</template>
