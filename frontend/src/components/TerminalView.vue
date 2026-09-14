<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref, watch } from "vue";
import "@xterm/xterm/css/xterm.css";
import { useTerminal, type ConnStatus } from "@/composables/useTerminal";
import { useUiStore } from "@/stores/ui";
import { hasFinePointer } from "@/utils/device";
import KeyBar from "./KeyBar.vue";

const props = defineProps<{ sessionId: string }>();
const emit = defineEmits<{ (e: "status", s: ConnStatus): void }>();

const ui = useUiStore();
// keyboardOpen is inferred from a shrinking viewport, so on a desktop it is
// also true whenever the window loses height — docking devtools to the bottom
// is the everyday case. The key bar is phone chrome, so it takes the same
// pointer gate AppShell.vue puts on the sidebar and BottomNav. Read once on
// mount: pointer capability doesn't change from a resize or a rotation.
const touchPrimary = !hasFinePointer(window);
const host = ref<HTMLElement | null>(null);
const { status, mods, open, connect, dispose, toggleMod, pressSpecial, focus } =
  useTerminal();

watch(status, (s) => emit("status", s));

onMounted(async () => {
  if (host.value) {
    // Awaited: open waits for the terminal's symbol font (issue #46) and the
    // socket has nothing to write into until it has built the terminal.
    await open(host.value);
    connect(props.sessionId);
    // Mount *is* "the session became active": TerminalPage keys this component
    // on the route id, so switching sessions tears it down and builds a new
    // one. Only where there is a mouse (issue #25) — on a touch device,
    // focusing the terminal opens the virtual keyboard, and having it spring up
    // on every session switch is worse than one tap to start typing.
    if (hasFinePointer(window)) focus();
  }
});

onBeforeUnmount(() => dispose());
</script>

<template>
  <div class="flex h-full w-full flex-col">
    <div ref="host" class="terminal-host min-h-0 flex-1" />
    <KeyBar
      v-if="touchPrimary && ui.keyboardOpen"
      :mods="mods"
      class="shrink-0"
      @mod="toggleMod"
      @key="pressSpecial"
    />
  </div>
</template>
