# Mobile / Responsive Checklist

Manual acceptance checklist for the responsive UI. Test in Chrome device mode
(iPhone SE / iPhone 14 Pro) and iPad, plus a desktop window resized across the
breakpoints. Breakpoints follow Tailwind defaults: `sm=640px`, `lg=1024px`.

## Layout by width

- [ ] **≥1024px (lg):** persistent sidebar with brand, nav labels, and the
      session quick-list is visible beside the content.
- [ ] **640–1023px (sm):** sidebar collapses to an icon rail (no labels, no
      quick-list); content fills the rest.
- [ ] **<640px:** sidebar is hidden; a fixed bottom navigation bar
      (Dashboard / Terminal / Settings) is shown instead.

## Bottom navigation (<640px)

- [ ] Bottom nav is fixed to the viewport bottom and does not overlap content
      (content has bottom padding).
- [ ] Each item is a ≥44px touch target.
- [ ] The active route item is highlighted in emerald.
- [ ] "Terminal" navigates to the current/most-recent open session, or the
      dashboard if none are open.

## Tab bar (terminal page)

- [ ] Open sessions appear as tabs above the terminal.
- [ ] The tab strip scrolls horizontally when tabs overflow; the page itself
      never scrolls horizontally.
- [ ] The active tab is underlined in emerald.
- [ ] The close (×) button removes the tab; closing the active tab navigates to
      an adjacent tab, or the dashboard if it was the last.
- [ ] Each tab is a ≥44px touch target.

## Terminal

- [ ] Terminal fills the available height on all widths (full-screen on phones,
      above the bottom nav).
- [ ] Rotating the device / resizing refits xterm and the PTY resizes to match.
- [ ] A one-finger drag scrolls the backlog with the finger — same distance, same
      direction, every swipe. Not double speed, not one line, not nothing
      (issue #64: two scrollers used to race, and the winner varied).
- [ ] **Start the drag on a character**, in the middle of a line of output, not
      on empty space. That gesture is the one issue #64 died on: xterm rebuilds
      a row's spans whenever it redraws the row, and the swipe used to stop the
      moment the character it began on was deleted. Landing on empty space
      worked all along, so a swipe that starts there proves nothing.
- [ ] Repeat that drag while the session is producing output (`yes`, or a build):
      scrolling stays as steady as it is on an idle session.
- [ ] A flick keeps scrolling and coasts to a stop; a slow drag that pauses
      before the finger lifts does not.
- [ ] At the top and bottom of the scrollback the gesture stops there — no
      pull-to-refresh, no back-swipe, no rubber-band on the page.
- [ ] A tap still focuses the terminal and opens the keyboard.
- [ ] Run a full-screen program — `less /etc/services`, `htop`, or an editor —
      and drag: it scrolls, the same as a mouse wheel does on a desktop. The
      alternate screen has no scrollback, so the drag reaches the program as
      cursor keys or as a mouse report instead.
- [ ] Leaving that program returns the drag to the backlog, with the scrollback
      where it was.
- [ ] "Disconnected — reconnecting…" overlay appears on connection loss and
      clears on reconnect.
- [ ] "Session ended" banner appears when the shell exits.

## Keyboard

- [ ] Type a word letter by letter: it reaches the shell once, whole, with no
      half-typed prefix ahead of it (issue #22).
- [ ] Tap a suggestion above a half-typed word: the suggestion arrives, the
      prefix does not.
- [ ] **Swipe (glide) two words in a row**: `hello` then `world` arrive as
      `hello world`. The keyboard writes that space itself, once it can see what
      precedes the cursor — which is why the delivered tail is parked back in
      the helper textarea between words (issue #82).
- [ ] Swipe a word, then tap a suggestion to correct it: the line ends up with
      the corrected word only, not both. A suggestion replaces what was already
      sent, so it goes out as DEL plus the replacement.
- [ ] Swipe a word and then press Enter straight away: the word arrives, then
      the newline, in that order.

### Reproducing the swipe fault without a phone

Chromium can drive a real composition over CDP, which is trusted input and so
runs the same handlers a keyboard does — unlike events dispatched from page
JavaScript, which are `isTrusted: false` and take a different path. That is what
found #82:

```js
const cdp = await page.context().newCDPSession(page)
await cdp.send('Input.imeSetComposition', { text: 'hello', selectionStart: 5, selectionEnd: 5 })
await cdp.send('Input.insertText', { text: 'hello' })  // commits the word
await cdp.send('Input.insertText', { text: ' ' })      // the keyboard's trailing space
```

Watch what leaves, not what the app thinks it sent: wrap `WebSocket.prototype.send`
in an init script and decode the payloads. Playwright is deliberately **not** a
project dependency (§2 rules out an E2E framework), so this lives in a scratch
directory when it is needed and does not ship.


## On-screen key bar ("Keys")

- [ ] Tapping "Keys" in the bottom nav shows the bar above the bottom nav and
      tapping it again hides it.
- [ ] **On a screen narrower than the bar, the page does not get wider.** The
      bar's two rows scroll sideways under a finger instead; everything else —
      the tab strip, the files button in the terminal's top-right corner — stays
      where it was. Opening the bar used to stretch the terminal column to the
      bar's own width, which the app shell then clipped, so whatever sat past
      the screen edge was simply gone (no scrollbar, nothing to swipe).
- [ ] A sticky modifier (Ctrl/Alt/Shift) highlights while armed and clears after
      the next key.
- [ ] Pressing a key does not close the soft keyboard.

### Checking widths without a phone

The width faults above are measurable, so they need no eye and no device. Drive
the real app in Chromium at a phone viewport, open the key bar, and compare what
the layout produced against the viewport:

```js
const ctx = await browser.newContext({ ...devices['Pixel 5'] })  // pointer: coarse, hover: none
// …log in, open /sessions/<id>, tap "Keys"…
await page.evaluate(() => {
  const col = document.querySelector('main .flex-1 > .relative')
  const row = document.querySelector('main .select-none.border-t > div')
  return {
    innerWidth: window.innerWidth,                  // 393
    terminalColumn: col.getBoundingClientRect().width,  // must equal innerWidth
    rowScrollable: row.scrollWidth > row.clientWidth,   // must be true
  }
})
```

A column wider than the viewport is the fault; a row whose `scrollWidth` equals
its `clientWidth` is the same fault seen from the other side, because a scroller
as wide as its content has nothing left to scroll. Note that `documentElement
.scrollWidth` stays at the viewport width either way — the app shell's
`overflow-hidden` clips the overflow rather than letting the page scroll — so it
is not the thing to assert on.

Same scratch-directory rule as the swipe recipe above — the browser is installed
globally in the devcontainer, nothing about it belongs in `frontend/package.json`.


## Dashboard

- [ ] Session cards reflow: 1 column (<640px), 2 (sm), 3 (lg).
- [ ] The card grid scrolls vertically; the page never scrolls horizontally.
- [ ] "New session" dialog is usable at 320px width; inputs are full-width and
      selects are tappable.

## Chrome / theme

- [ ] Dark theme throughout; no light flashes on navigation.
- [ ] Browser tab shows the sessile favicon and a route-specific title
      ("sessile — Sessions/Terminal/Settings").
