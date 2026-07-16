# Mobile / Responsive Checklist (M5)

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
- [ ] "Disconnected — reconnecting…" overlay appears on connection loss and
      clears on reconnect.
- [ ] "Session ended" banner appears when the shell exits.

## Dashboard

- [ ] Session cards reflow: 1 column (<640px), 2 (sm), 3 (lg).
- [ ] The card grid scrolls vertically; the page never scrolls horizontally.
- [ ] "New session" dialog is usable at 320px width; inputs are full-width and
      selects are tappable.

## Chrome / theme

- [ ] Dark theme throughout; no light flashes on navigation.
- [ ] Browser tab shows the sessile favicon and a route-specific title
      ("sessile — Sessions/Terminal/Settings").
