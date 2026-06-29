# vterm / vibecli terminal rebuild

Status: DESIGN (no production code changed yet). Owner: autonomous rebuild session started 2026-06-29.

This is the single source of truth for the brick-by-brick rebuild of the vterm terminal
viewer and its vibecli consumer. It exists so that every learning, decision, and
justification survives context compaction or a crashed session. If you are resuming this
work with no other memory, read this file top to bottom first.

The rule for this document: every architectural choice records _why_, not just _what_.
When a decision changes, append to the Decisions Log with the date and the reason; do not
silently rewrite history.

---

## 0. North star

An accurate, reliable terminal _viewer_. The terminal's job is to show exactly what the
program (almost always kiro-cli's Ink TUI) drew, keep the reading position the user
expects, never lose or duplicate output, and feel native to touch and mouse. Everything
else is secondary. Performance and cleverness lose to correctness every time.

The user's framing, preserved verbatim in intent: the visible bugs are "the smoke that
hides the fire of a messy architecture." We fix the fire (the architecture), and the smoke
(the bugs) clears as a consequence. We build "brick by brick until the full wall is rock
solid," not another layer of patches.

---

## 1. The seven bugs (the smoke)

1. Select / copy / paste during printing or when static does not work on iOS touch; the
   selection callout / our context menu shows off-screen.
2. WebSocket sleep/wake: if the iPhone sleeps and is reopened, the feed reconnects but
   anything printed during the sleep window is missing until a manual refresh.
3. The view jumps around instead of sticking to the bottom.
4. Scrolling up to read keeps getting interrupted by newly printed text instead of holding
   position.
5. Past text sometimes gets appended multiple times, making a mess when scrolling.
6. On iOS, touch scroll does not work until the contenteditable div has been selected once.
7. On a sparse first load (little content), part of the screen cannot be tapped to summon
   the keyboard.

## 2. The one fire

A terminal is one ordered stream of lines with a small writable window at the tail. The
current code models it as two different things, end to end, and reconciles the two in four
separate places using heuristics and timers. Every reconciliation point is a bug surface.

The split, traced through the stack:

- Server: a `vt.Screen` (the live grid) plus a separate `scrollbackRing` (history). Two
  buffers, no shared line identity.
- Wire: `MSG_SCREEN` (changed rows of the live grid) versus `MSG_SCROLL` (lines that fell
  off the top). Two message types for what is really one numbered sequence.
- Client DOM: a "live zone" of the last N rows repainted in place, plus frozen history rows
  above it. Two regions in one contenteditable column.
- Resume: the client sends `scrollbackHave` as a _count_ of rows it holds, and the server
  replays `ring[count:]`. A count used as if it were a stable offset.

On top of the split, one DOM element (`#term-output`, contenteditable) is forced to do four
contradictory jobs at once: live viewport, history log, text-selection surface, and iOS
keyboard proxy. The contradictions are mediated by a pile of heuristics (a 100px bottom
tolerance, a 150ms scroll debounce, a 60-second touch window, a 350ms viewport settle) and
four uncoordinated writers of `scrollTop`. Each heuristic is wrong in some condition, which
is the "patchy mess" the user feels.

That is the whole diagnosis in one paragraph. The rest of this document is the proof and the
rebuild.

---

## 3. Root-cause analysis, bug by bug

Each bug is traced to specific code. File references are to the current tree at the start of
this rebuild. The point is to show that the seven bugs collapse into a handful of
structural causes.

### Cause A: no single owner of scroll position (bugs 3, 4)

`render.ts` runs everything in one `requestAnimationFrame` (`flushAll`) that does four jobs
in sequence:

1. Insert pending scrollback above the live zone, with manual scroll-anchor math
   (`rowAtViewportTop`, reading `offsetTop` then writing `scrollTop`).
2. `flushScreenInner` repaints changed live rows, then recomputes the live zone's visible
   height as `visibleEnd = max(cursor+1, lastNonEmpty+1)` to trim trailing blank rows.
3. A per-frame caret hack: when focused, collapsed, and not scrolled up, it runs
   `selection.selectAllChildren(output)` then `collapseToEnd()`. The code comment itself
   admits this triggers a caret scroll-into-view.
4. `stickToBottomIfFollowing()` sets `scrollTop = scrollHeight`.

So `scrollTop` has at least four writers per frame: the anchor restore, the caret
scroll-into-view, the stick-to-bottom, and (separately) the viewport-settle handler in
`viewport.ts`. They race. Worse, step 2 makes the live zone's height oscillate as the Ink
TUI redraws (a spinner line appears and disappears, a menu opens and collapses), so
`#term-output` `scrollHeight` oscillates frame to frame, so the history above visibly jumps
even when the user is holding still. That is bug 3. Bug 4 is the same oscillation plus the
caret scroll-into-view fighting the user's upward scroll, with the `isInUserScroll`
heuristic (a 60-second window armed on `touchstart`, reset to 150ms on `touchend`) as the
only brake.

The scroll module itself (`scroll.ts`) is a pure heuristic: `userScrolledUp = !(scrollTop +
clientHeight >= scrollHeight - 100px)`. The 100px tolerance, the 150ms debounce, the
60-second touch window, and a `suppressUntil` window for programmatic writes are all magic
numbers that each fail in some real scenario.

### Cause B: the contenteditable does four jobs (bugs 1, 6, 7)

`#term-output` is `contenteditable=true`, `user-select:text`,
`-webkit-user-modify:read-write-plaintext-only`, and is focused on load. Confirmed live
against the dev container (`activeElement = term-output`).

- Bug 1 (selection): the live zone is repainted with `replaceChildren` every flush, which
  destroys any selection anchored in a changing row. On top of that, the per-frame
  `selectAllChildren + collapseToEnd` caret hack (Cause A, step 3) actively collapses any
  selection the instant the user starts one. Selecting during streaming is therefore
  impossible. The context menu (`showCtxMenu`) sets `left/top` to the raw pointer
  coordinates with no viewport-edge clamping, so near the right or bottom edge it renders
  off-screen. That is the off-screen tooltip.
- Bug 6 (touch scroll dead until first selection): `#term-output` is a focused
  contenteditable. iOS routes the first touch-drag on a focused editable as a
  caret/selection gesture, not a scroll of the ancestor. After one selection interaction it
  starts behaving. Root cause is the editable being the scroll content.
- Bug 7 (sparse-load dead zones): on a near-empty first load, `#term-output` is only one
  line tall (confirmed live: 17px inside a 633px container). The dual-element focus model
  (focus the contenteditable output, or focus a 1px hidden textarea on tap) makes
  tap-to-summon-keyboard race iOS's own focus-for-caret, and the 616px of container below
  the 17px of output is ambiguous tap territory.

### Cause C: resume by count, not identity (bugs 2, 5)

There are no per-line sequence numbers anywhere in the system. On resume the client sends
`scrollbackHave` = (DOM rows it holds) and the server replays its ring buffer from that
offset (`ring[clamp(scrollbackHave):]`). The client caps history at 1000 rows; the server
caps its ring at 1000 lines; the two evict independently. The moment they diverge, a count
used as an offset misaligns:

- If the server has evicted lines the client still references, `ring[count:]` skips lines:
  missing content (contributes to bug 2).
- If a flush was still pending at disconnect, or a zombie socket delivered frames twice, or
  a second device touched the session, `ring[count:]` overlaps: duplicate rows (bug 5).

The protocol literally cannot tell "you already have line X" from "you are missing line X,"
because lines have no X.

### Cause D: reconnect is several uncoordinated events, not one state machine (bug 2)

`connection.ts` `reconnectNow()` early-returns when `connState.status === "connected"`. On
iOS wake, the socket is frequently a zombie: the OS froze it during sleep, and on wake it
still reads OPEN for a while before the close handshake completes. Both `visibilitychange`
(visible) and `pageshow` call `reconnectNow()`, but the early-return on "connected" means
neither tears down the zombie, so no resume is sent, so the lines printed during sleep are
never backfilled. The user sees a connected-looking terminal missing its middle. That is the
smoking gun for bug 2. There is no client-side staleness check; the server's ping loop is
server-to-client only and takes ~45s to declare a peer dead.

### Cause E: too many magic-number heuristics

Across `scroll.ts`, `render.ts`, and `viewport.ts`: 100px bottom tolerance, 150ms scroll
debounce, 60-second touch window, 350ms viewport settle, plus a `suppressUntil` programmatic
window. Each is a guess that papers over the absence of a real ownership model. They
interact in ways no one can hold in their head, which is why fixes to one bug reopen another.

### Why the bugs compound

Causes A and B share the same element. A fix to selection (B) perturbs the scroll writers
(A); a fix to stickiness (A) perturbs the caret (B); a fix to resume (C/D) changes what the
renderer (A) receives mid-stream. The split is the connective tissue that makes every patch
leak into a neighbour. Cut the tissue and the bugs stop being coupled.

---

## 4. Market research and precedent

Wide survey of how other open-source web terminals handle the same problems, with the
takeaway for this rebuild. Sources are cited inline. Content was rephrased for compliance
with licensing restrictions.

### The rendering camp split

There are two camps for drawing a terminal in a browser:

- Canvas / WebGL (xterm.js): draw cells to a pixel surface, fake the scrollbar, reimplement
  selection / copy / find in JS. Fast for huge buffers, but the browser has no idea there is
  text there.
- Real DOM rows (hterm, vibecli/vterm, and now Vercel's wterm): each terminal row is a real
  element with real text nodes. Native selection, copy, find, screen readers, and crucially
  native scrolling all work for free.

The decisive external data points:

- xterm.js touch scrolling on iOS is genuinely broken, not just unpolished. See xterm.js
  issues [#3613](https://github.com/xtermjs/xterm.js/issues/3613) (scroll breaks when the
  gesture starts on text), [#1007](https://github.com/xtermjs/xterm.js/issues/1007) (touch
  scroll should map to arrow keys), and [#1101](https://github.com/xtermjs/xterm.js/issues/1101)
  (mobile support). ttyd, wetty, and gotty all sit on xterm.js, so the entire turnkey
  web-terminal field inherits this limitation. This is exactly why the user rejected the
  xterm fixed-viewport route, and the rejection is correct.
- Vercel Labs shipped [wterm](https://betterstack.com/community/guides/scaling-nodejs/w-term-dom/)
  in 2026 rendering each row as a real `div.term-row`, using `requestAnimationFrame` with
  dirty-row tracking (only re-render rows that changed), specifically so native selection,
  copy/paste, Ctrl+F, and screen readers work without reimplementation. That is the same
  architecture vibecli/vterm already has. Independent convergence by a serious team is strong
  validation: the foundation is right, do not rip it out.
- hterm (ChromeOS Secure Shell) has rendered real DOM rows with native scrolling for years.
  Same camp.

Takeaway: keep real DOM rows, dirty-row diffing, and native scroll. The rebuild is in the
layers above the renderer, not the renderer's fundamental choice.

### Reconnect and resume: mosh

mosh synchronizes terminal _state_, not a byte log. It keeps the terminal state on both ends
and reconciles to the current state on reconnect, which is why it never duplicates or
corrupts across a network change (USENIX ATC 2012, "Mosh: An Interactive Remote Shell for
Mobile Clients"). Its known weakness is that it has no scrollback at all
([mosh#122](https://github.com/mobile-shell/mosh/issues/122)). A later extension, "stable
numbers," adds consistent absolute line indices across reconnects so coordinates stay aligned.

Takeaway: state-sync the live screen (robust, dup-free) and give every line an absolute index
so history alignment on resume is exact. This is the bridge between mosh's reliability and the
scrollback the user needs. It directly motivates Cause C's fix.

### Sticky-bottom: the chat-log field

The common patterns: `flex-direction: column-reverse` to pin content to the bottom natively
(with known scrollbar and reverse-DOM caveats), or a tolerance-based "am I at the bottom"
check (commonly 8 to 24px) combined with writing `scrollTop = scrollHeight` only when the
user is already following. The browser's native scroll anchoring (`overflow-anchor: auto`,
the default) holds the viewport position when content is inserted above the anchor. vibecli
currently sets `overflow-anchor: none` (confirmed live) and does manual `offsetTop` math
instead, which is Cause A.

Takeaway: lean on the browser. Re-enable scroll anchoring, use a small bottom tolerance, and
have exactly one place decide "follow" versus "hold."

### iOS platform constraints

- `.focus()` only summons the soft keyboard inside a user-gesture window; calling it later
  silently fails to open the keyboard.
- Focusing a fixed-position or editable element makes iOS scroll it into view, fighting a
  custom scroll container.
- The Visual Viewport API is the correct tool for keyboard inset handling; `viewport.ts`
  already uses it.

Takeaway: summon the keyboard from a real input element, inside the pointer gesture, and keep
that element off the scroll path.

---

## 5. Design principles and constraints

These are fixed inputs to the architecture. Violating one of these is a design bug.

### 5.1 Hard constraints from the user

1. Preserve native touch scrolling. Real DOM rows in a natively-scrolled container. No
   JS-faked scrollbar, no canvas. This is why xterm was rejected and the reason is sound.
2. Preserve native text selection across the whole buffer, including history.
3. Keep the two properties the contenteditable was chosen for, even if the element changes:
   - It summons the iOS virtual keyboard naturally.
   - It gives a local typing buffer so keystrokes survive a bad connection and are not lost.
4. The focus is an accurate, working terminal viewer. Not a feature surface.

### 5.2 The Ink mapping (why the live/history split existed, and why the rebuild keeps its intent)

The user's recalled rationale for the original live/history split: the live section is
reloaded fresh repeatedly to allow full content replacement (sections close and visually
delete, temporary menus appear with options then collapse after selection, live animations);
the history is locked for performance so it is not re-rendered.

This maps almost exactly onto how kiro-cli's TUI actually works. kiro-cli renders with Ink
(its TUI is `tui.js`, confirmed in the kiro-cli research notes). Ink has two output regions:

- A `<Static>` region: lines Ink has committed. Ink writes them once and never touches them
  again; as more output arrives they scroll up out of the dynamic region. This is history.
- A dynamic region: the live frame Ink re-renders on every state change. Ink erases it
  (cursor-up plus erase-line / erase-display) and redraws it. Menus, spinners, progress bars,
  the goal panel, the queue-steering tray, and the `/rewind` picker all live here. This is the
  live zone, and it genuinely takes arbitrary full-frame rewrites.

So the original split was tracking Ink's own Static/dynamic boundary. The insight for the
rebuild is that we do not need two _data models_ to honor that boundary; we need one model
that knows which lines are still mutable. The absolute-line-index model (section 6) does
exactly this: lines inside the screen window are mutable (Ink's dynamic region, rewritten
freely and idempotently), and lines below the window are frozen (Ink's `<Static>` commits).
The boundary is the VT scroll operation, and the server owns it. We keep the user's intent
(live region takes any change, history is cheap and stable) while removing the dual-model
reconciliation that caused the bugs.

### 5.3 Ink redraw fidelity: empirically verified low-risk

The danger the user flagged: kiro-cli/Ink has "many quirks around reloading content." If the
VT emulator mishandles a sequence Ink emits, the server's screen buffer diverges from what
Ink intended, and the wire then faithfully transmits a wrong screen. No viewer-layer redesign
can fix a wrong buffer. So this was verified empirically: capture the real PTY byte stream from
a live kiro-cli session on the dev container (the `/debug/raw` ring) and tally the ANSI control
sequences it actually uses (capture 2026-06-29; method in section 12). What kiro-cli's Ink TUI
emits, and the verdict for each:

- Synchronized output / DEC mode 2026 is used heavily (≈15 `?2026h`/`?2026l` pairs in seconds).
  It IS handled: `vt/csi.go` maps `?2026h` to `HoldFlush(now+1s)` and `?2026l` to
  `ReleaseFlush()`, so the flush builder skips partial frames during a synchronized update and
  sends the complete frame on release. No tearing. (An earlier draft of this section claimed
  2026 was unhandled; that came from a flaky search tool and is wrong. It is handled, and it
  composes with absolute indexing: lines that drain during the hold commit at their absolute
  indices on the next post-release build.)
- Scroll regions (DECSTBM `r`) and insert/delete-line (IL/DL `L`/`M`): the capture shows ZERO
  of these. Ink positions with cursor-relative moves plus erase, never a scroll region, so the
  concern is empirically moot for kiro-cli.
- Cursor-relative moves CUU (`A`), CUD (`B`), column-absolute CHA (`G`), CUP (`H`); erase-line
  EL (`K`, the workhorse, ≈67/sample); erase-display ED (`J`, a couple of full clears at
  startup); SGR (`m`, ≈625). All handled by `vt/csi.go`. The distinct CSI final bytes seen were
  exactly `m K l G h A B J H` — all standard, all handled.
- Cursor hide/show (DECTCEM `?25l`/`?25h`, ≈19 hides): Ink hides the cursor while painting. The
  wire carries a cursor-hidden flag; honored.
- NOT used by normal kiro-cli chat: the alternate screen (`?1049` = 0). So the ephemeral
  alt-grid path is rarely exercised in practice, but it must stay correct for a sub-tool that
  switches (an editor, a pager).

Verdict: the VT core handles every sequence kiro-cli's Ink emits; "the core works and is good"
holds under measurement. The one residual is the alt-screen path (chat never triggers it);
brick 1's ephemeral-grid model covers it, and it gets a live check with a real editor session
during brick 3.

### 5.4 Engineering constraints

- vterm is a shared library; vibekit also consumes the Go `terminal`/`vt` packages and the TS
  `render`/`scroll`/`connection` modules and the wire protocol. The user has explicitly waived
  blast-radius concern ("GitHub is our backup"), so we rebuild in vterm and update both
  consumers, accepting a breaking wire v2. We do not fork into vibecli (that would reintroduce
  the cross-language drift hazard the shared library exists to prevent).
- Wire v2 is a clean break. The Go server and TS client ship in lockstep from one repo, and we
  control both consumers, so there are no external clients to keep compatible.
- Experiments build the vibecli image locally on Borgcube and deploy to the `vibecli-dev`
  container, not through GitHub Actions (homelab is out of Actions budget, and the user
  cleared manual builds with admin override). Keep experiments off the permanent record until
  a brick is proven.

---

## 6. The architecture

The centerpiece is resolving the live/history split. The rest follows from it.

### 6.1 One buffer, absolute line indices (the resolution)

Stop treating live and history as different things. Address every line by a monotonic
absolute index and let the writable window slide along it.

Server model:

- Every line that ever exists has a stable absolute index: 0, 1, 2, growing forever within a
  session. The screen's top row sits at absolute `base`; screen row `y` is absolute
  `base + y`. When the screen scrolls up by one (a new line pushed at the bottom, the top
  line leaves the screen), `base` increments and the line that left is now frozen at its
  absolute index. History and screen are the same numbered sequence; "history" just means
  "index below `base`."
- One message carries content: `setLine(N, runs)`. While a line is inside the window it gets
  re-sent as it redraws (Ink's dynamic region); once it scrolls below `base` it is sent its
  final form once and never touched again (Ink's `<Static>` commit). The `MSG_SCREEN` versus
  `MSG_SCROLL` distinction disappears at the data level.
- Cursor position, DEC modes, and title remain their own small messages (they are not line
  content).
- The alternate screen (vim, htop, full-screen apps that use `?1049h`) becomes a separate
  ephemeral grid keyed by viewport row, with no history accrual, cleared on exit. This also
  removes the alternate-screen scrollback duplication that even xterm.js suffers
  ([xterm.js#802](https://github.com/xtermjs/xterm.js/issues/802)). Note: Ink in kiro-cli
  generally does not use the alternate screen for normal chat; it renders inline and scrolls.
  So most sessions never touch the ephemeral grid, but it must exist for correctness when an
  app does switch.

Why this kills the bugs at the root, not by heuristic:

- `setLine(N, ...)` is idempotent. Writing the same index twice overwrites the same row.
  Duplication (bug 5) becomes structurally impossible.
- A gap in indices is detectable. Missing content (bug 2) becomes a visible, handleable
  condition instead of a silent misalignment.
- There is no live-versus-history code path in the renderer. There is only "apply line N":
  find or create the row for N, rebuild its spans. The reconciliation tissue (Causes A, C) is
  gone.

Client model:

- One line store keyed by absolute index, capped (target 5000), evicting from the top and
  remembering the lowest retained index. No live/history branches.
- The screen window is a fixed block of exactly `rows` line-slots at the bottom, always
  present at full height. We never trim trailing blank rows (the Cause A oscillation source).
  `scrollHeight` then changes only when a real history line commits, which is exactly a real
  scroll event. Bug 3's oscillation is gone by construction.

### 6.2 Resume by index (replaces resume by count)

On reconnect the client sends: "I hold authoritative content through absolute index `H`, my
screen base is `B`, server epoch I last saw is `E`." The server responds:

- If its epoch differs from `E`, it is a fresh process: full reset, client clears and repaints
  from the new screen.
- Otherwise it sends every retained line with index greater than `H`, plus the current screen
  window, plus cursor and modes. Indices align deterministically; no slicing ambiguity.
- If the server has evicted below `H + 1` (the client was gone long enough that needed history
  is gone), it sends an explicit gap marker (lowest available index). The client shows an
  "earlier output trimmed" affordance instead of silently stitching misaligned lines.

This is the mosh lesson applied: align by absolute identity, reconcile to current state.

### 6.3 Separate the four jobs of the contenteditable

The contenteditable was overloaded. Split the jobs onto elements that each do one thing.
This is "Shape 1" and it preserves the two properties the user cares about.

- `#term` (the fixed, full-viewport, `overflow:auto` container) is the scroll-and-tap
  surface. Native scroll, native momentum. Not editable, not focusable. A tap anywhere on it
  focuses the input element inside the pointerup gesture. Because it always fills the viewport
  there are no dead zones (fixes bug 7), and because it is not editable the first touch-drag
  scrolls instead of placing a caret (fixes bug 6).
- `#term-output` is display-only rows: `user-select:text`, not contenteditable, not
  focusable. Native long-press selection still works (that is how selecting any normal web
  page text works on iOS). Frozen history rows are never re-rendered, so a selection in
  history is never disturbed (fixes the worst of bug 1). We delete the per-frame
  `selectAllChildren + collapseToEnd` caret hack entirely; it was the active selection killer.
- `#term-input` is a real `<textarea>`: it summons the keyboard (the canonical, most reliable
  way on iOS), holds the local typing buffer, and handles IME composition. Combined with the
  existing outbox (byte-level durability across reconnects) and predictive echo, the user's
  "inputs survive a bad connection" property is preserved, arguably strengthened. The textarea
  is small (cursor-sized, positioned at the cursor) so it never covers the scroll surface and
  never eats scroll or selection.

The key realization: contenteditable was not wrong, it was overloaded. A humble textarea gives
both properties the user wanted (keyboard summon, local buffer) once the display and selection
jobs move off it onto real rows and the native container.

Open sub-question to validate live: whether a 1-line textarea at the cursor is enough for IME
on all targets, or whether the input element needs to be larger during composition. The
fallback (Shape 2) is a small contenteditable used only as the current input line, never as
the output. Decision deferred to live IME testing; recorded in the Decisions Log.

### 6.4 One scroll controller

Exactly one module owns `scrollTop`. Two states:

- Following: the user is at (or within a small tolerance of) the bottom. After a frame that
  committed new history lines, set `scrollTop = scrollHeight` once, at end of frame.
- Holding: the user has scrolled up. Do nothing to `scrollTop`. Re-enable native
  `overflow-anchor` so insertions above hold the user's position automatically.

Transitions are driven by the scroll event with a single small tolerance, and by an explicit
"jump to bottom" control. No caret scroll-into-view (the textarea is off the scroll path and
tiny). No viewport-settle writer competing (the controller reads the follow state before a
keyboard-driven resize and restores it after). The heuristic soup of Cause E collapses to one
tolerance and one state bit.

### 6.5 One connection state machine

Replace the several uncoordinated reconnect triggers with one idempotent `ensureFresh()`:

- It is safe to call repeatedly. `visibilitychange`, `pageshow`, a staleness timer, and manual
  retry all call the same function.
- On wake it does NOT trust `connState`. It assumes the socket may be a zombie and tears it
  down unconditionally, then connects and resumes (fixes bug 2's smoking gun, Cause D).
- A client-side liveness check (last-frame-received timestamp, plus a lightweight client ping)
  detects a stale socket without waiting ~45s for the server ping loop.
- Resume uses absolute indices (6.2), so the lines printed during sleep are backfilled exactly
  on wake.
- The outbox (unacked input bytes, retransmitted on reconnect) is retained for input
  durability; it already works and is orthogonal to the screen model.

### 6.6 Wire protocol v2 (sketch; full spec lands with brick 1)

Binary, little-endian, one socket. Server-to-client frames carry a small common header
(message type, and the input-ack the outbox needs). Message types:

- `setLine`: absolute index (uint64 or a session-relative uint32 with wrap handling, decided
  in brick 1), then the row payload (styled runs, reusing the existing run encoding: text,
  fg, bg, attrs, underline color, OSC 8 URL). Idempotent.
- `screen`: the current window descriptor (base index, rows, cols, cursor row/col/style/flags).
  Sent when the window geometry or cursor changes; line contents come via `setLine`.
- `altLine` / `altScreen`: ephemeral alternate-screen grid updates keyed by viewport row.
- `modes`: DEC private mode state (bracketed paste, app cursor, mouse mode, focus reporting,
  app keypad, reverse video). Unchanged in spirit from v1.
- `title`: OSC 0/1/2 window title.
- `resumeAck`: server epoch and the lowest available index (for gap detection).

Client-to-server: raw input bytes (unframed), plus `0x00`-prefixed JSON control messages
(`resize`, and `resume {sessionId, haveThrough, screenBase, epoch}`).

The exact byte layout, the index width decision, and the synchronized-frame batching question
are resolved in brick 1 and the spec is written into `WIRE_PROTOCOL.md` (v2) at that time. The
cross-language pairing rule applies: the Go encoder and TS decoder land in the same change.

---

## 7. The bricks (implementation plan)

Bottom-up. Each brick has one owner, enforces its own guards, is independently testable, and
is verified live before the next is laid. A brick is not "done" until its tests pass and its
target bugs are demonstrably gone in the live harness.

1. Line model and wire v2. Go: expose absolute indexing from `vt.Screen` (a monotonic
   committed-line counter and `base`), rework `terminal/` frame generation to emit `setLine` /
   `screen` / `altLine`, rework scrollback to retain by absolute index, rework resume to align
   by index with a gap marker. TS: decoder for v2. Write `WIRE_PROTOCOL.md` v2. Land Go encoder
   and TS decoder together. Kills the protocol half of bugs 2 and 5.
2. Client line store. One absolute-indexed buffer, capped, idempotent apply, gap-aware. Pure
   data structure, unit-tested in isolation. No DOM.
3. DOM renderer. Rows keyed by `data-abs`, dirty-row update, fixed-height screen-window block,
   never trims trailing blanks. Display-only. Kills bug 3's oscillation.
4. Scroll controller. Single owner of `scrollTop`, Following/Holding, native `overflow-anchor`,
   one tolerance. Kills bugs 3 and 4.
5. Input / keyboard / selection separation. `#term` scroll+tap surface, `#term-output`
   display+select only, `#term-input` textarea for keyboard+buffer+IME. Viewport-clamp the
   context menu. Kills bugs 1, 6, 7.
6. Connection state machine. One idempotent `ensureFresh()`, unconditional wake teardown,
   client liveness check, index resume backfill, outbox retained. Kills bug 2 fully.
7. Guard hardening and cleanup. Wire in the full guard sets (section 8), remove the dead
   heuristics (Cause E), delete superseded code paths, reconcile vibekit.

Ordering rationale: data correctness first (1, 2), then what the user sees (3, 4), then how the
user touches it (5), then resilience (6), then hardening (7). Each lower brick is verifiable
without the ones above it.

vibekit reconciliation happens as part of bricks 1 and 7: it consumes the same Go packages and
TS modules, so its shell panel updates to wire v2 and the new client API. The user has waived
blast radius, so this is in-scope, not deferred.

## 8. Guard sets

The user asked for many guards: "before appending new content do 10 checks; when ws reconnects
do 5 checks; 100% reliable." These are the concrete guard sets. They are assertions with
defined fallbacks, not silent no-ops.

### 8.1 Before applying a line (the apply-line guards)

1. Index is a finite, non-negative integer.
2. Index is greater than or equal to the lowest retained index (else it is below our window;
   drop, it is older than we keep).
3. Index is less than or equal to `base + rows - 1` for a live write, or exactly the next
   expected history index for a commit; an index beyond `base + rows` is a protocol error,
   logged and dropped.
4. Gap check: if index is greater than (highest known index + 1), record a gap and request
   backfill rather than inserting a floating row.
5. Idempotency: if the row already exists and the new content is byte-identical to what is
   rendered, skip the DOM write entirely (no churn, no selection disturbance).
6. Alt-screen consistency: a `setLine` while in alternate-screen mode is a protocol error
   (alt uses `altLine`); log and drop.
7. Run payload integrity: run count and per-run lengths are within bounds; a malformed run is
   dropped, not partially rendered.
8. Cell width: the rendered text width matches the expected column count (wide chars, combining
   marks); mismatch is logged for the Ink-fidelity audit.
9. Row element integrity: the target row element, if it exists, is the one our store points to
   (no orphaned node from a prior render).
10. Cap enforcement: after apply, if the store exceeds the cap, evict from the top and advance
    the lowest-retained index in one place.

### 8.2 On reconnect / resume (the resync guards)

1. Epoch: compare server epoch to last seen. Mismatch means a fresh server; full reset and
   repaint, do not attempt index alignment.
2. Index alignment: server's lowest-available index is less than or equal to our
   `haveThrough + 1`. If not, there is a gap; show the trimmed-history affordance and accept
   the gap rather than misstitching.
3. Window sanity: returned `base` and `rows` are consistent with the geometry we last sent
   (`resize`); if the server has a different size, adopt it and reflow.
4. Outbox versus input-ack: trim the outbox to the server-confirmed byte count, retransmit the
   remainder; if the ack exceeds what we sent, log and clamp (never go negative).
5. Cursor and modes: adopt the server's cursor and DEC modes from the resume response before
   resuming live frames, so the first post-resume frame is interpreted under the right modes.

These run in a fixed order at one place in the connection state machine. Each has a defined
fallback (reset, gap affordance, clamp); none silently continues on a violated invariant.

---

## 9. Decisions log

Append-only. Each entry: date, decision, why, and what it rules out.

- 2026-06-29 Keep real DOM rows + native scroll; do not adopt xterm/canvas. Why: xterm touch
  scroll is broken on iOS (issues cited in section 4) and the user requires native touch;
  hterm and Vercel wterm validate the DOM-rows approach. Rules out: canvas/WebGL rendering, a
  JS-faked scrollbar, virtualized off-DOM scrollback.
- 2026-06-29 Resolve the live/history split via one absolute-line-index buffer, not two data
  models. Why: a terminal is one numbered line stream; the dual model is the connective tissue
  that couples the bugs; absolute indices make dedup and gap-detection structural (mosh stable
  numbers). Rules out: count-based resume, separate MSG_SCREEN/MSG_SCROLL data paths, a
  client-side live-zone-versus-history branch.
- 2026-06-29 Treat the alternate screen as a separate ephemeral grid, not part of the indexed
  history. Why: alt-screen content must not accrue scrollback (it is the xterm #802 dup bug);
  keeps the indexed buffer purely the normal-screen line stream.
- 2026-06-29 Separate the contenteditable's four jobs (Shape 1): `#term` scroll+tap,
  `#term-output` display+select, `#term-input` textarea for keyboard+buffer+IME. Why: the
  overload is the shared cause of bugs 1, 6, 7; a textarea preserves the two properties the
  user named (keyboard summon, local buffer) without doing display/selection. Rules out: one
  element for everything. Fallback if IME needs it: a small contenteditable input line
  (Shape 2), decided after live IME testing.
- 2026-06-29 One scroll controller owning `scrollTop`, two states, native `overflow-anchor`
  re-enabled. Why: four racing writers and disabled anchoring are Cause A; the browser already
  solves position-hold-on-insert. Rules out: per-frame caret scroll-into-view, manual
  `offsetTop` anchor math, multiple writers.
- 2026-06-29 One idempotent `ensureFresh()` reconnect, unconditional teardown on wake. Why: the
  early-return-on-"connected" zombie socket is bug 2's smoking gun (Cause D). Rules out:
  trusting `connState` on wake, uncoordinated `visibilitychange`+`pageshow` triggers.
- 2026-06-29 Breaking wire v2, rebuild in vterm, update both vibecli and vibekit. Why: user
  waived blast radius ("GitHub is our backup"); forking would reintroduce drift. Rules out: a
  vibecli-local fork of the engine, backward-compat shims.
- 2026-06-29 (corrected after empirical capture) Synchronized output (DEC 2026) is already
  handled by the VT core (`vt/csi.go`: `?2026h`→HoldFlush, `?2026l`→ReleaseFlush), so frames
  are batched and there is no tearing. An earlier entry here called it an unhandled
  enhancement; that was based on a flaky search tool and is withdrawn. No work needed; it
  composes correctly with absolute indexing.
- 2026-06-29 (brick 1) Wire v2 keeps TWO server frame shapes (screen window + scroll history),
  both now carrying absolute indices (`base` on screen, `first_index` on scroll), funnelling
  into ONE client store. Why: lower churn on the proven flush-diff than collapsing to a single
  `setLine` message, and it achieves the same goal that matters (the client has one
  absolute-indexed buffer, no live/history branch). The wire framing is an encoding detail;
  the client data model is what was unified. Rules out: a second message type; it does NOT
  reintroduce the client-side split.
- 2026-06-29 (brick 1) Index width is uint64. Why: never wraps in practice, simplest to reason
  about; +8 bytes per frame is negligible at 50ms flush cadence. Rules out the session-relative
  uint32 + wrap-handling alternative.
- 2026-06-29 (brick 1) The scroll (history) frame is still sent to live clients, not only on
  resume. Why: a fast burst can scroll more lines than the window holds between two 50ms
  flushes, so those lines never appear as window rows and must be delivered as committed
  history. Re-delivering a line that WAS a window row is harmless because applying by absolute
  index is idempotent.
- 2026-06-29 (brick 1) Resume now COMMITS any pending drain to history at its absolute index
  instead of discarding it (v1 discarded on resume and on resize). Why: a scrolled-off line is
  history and must be retained for index-aligned replay; discarding it created the gaps that
  fed bug 2.
- 2026-06-29 (brick 1) Client resume `haveThrough` is temporarily hard-coded to -1 (full
  retained replay) until brick 6 wires the store-backed value. Why: safe and correct now
  (idempotent apply means full replay never duplicates), keeps the TS compiling before the
  client store (brick 2) exists.
- 2026-06-29 (brick 6) `reconnectNow()` made unconditional — it no longer early-returns when
  `connState` is "connected". On any wake trigger it aborts+closes whatever socket exists and
  reconnects. Why: the early-return trusted a zombie socket on iOS wake (reads OPEN but is
  dead) — bug 2's smoking gun (Cause D). This IS the design's `ensureFresh()`. Kept the name
  `reconnectNow` rather than renaming to avoid a churny cross-repo rename mid-rebuild (vibekit
  reconcile is brick 7). All wake/staleness triggers funnel into it; `connect()` is the
  fresh/initial entry; `scheduleReconnect()` is the post-close backoff. Rules out: trusting
  `connState` on wake.
- 2026-06-29 (brick 6) `haveThrough` on resume is now the store-backed highest absolute index
  (`render.getHighestIndex` via a new `getHaveThrough` callback wired in vibecli `app.ts`), not
  the temporary -1. The server replays exactly lines > haveThrough, so sleep-window output
  backfills on wake; idempotent absolute-index apply means re-delivery never duplicates.
- 2026-06-29 (brick 6) Added a client-side liveness probe (app-level ping/pong) rather than
  relying only on wake events. Why: a socket can go silently half-open with no
  visibilitychange/pageshow (a NAT/idle timeout, a flaky path); the server's WS-protocol pings
  are invisible to JS, so the client cannot see them. The probe distinguishes "idle but alive"
  (a pong returns) from "dead" (no reply), so a quiet terminal is never reconnect-flapped —
  which a naive "reconnect after N seconds of silence" rule would do. Timings: the heartbeat
  evaluates every 5s, probes after 10s of inbound silence, and declares the socket stale after
  a 7s unanswered grace, then calls reconnectNow(). ANY inbound frame counts as liveness
  (markActivity runs at the top of the message listener, before decode, so even a
  null-decoding frame counts); a hidden tab is skipped (wake events own it; hidden-tab timers
  are throttled). Rules out: silence-only staleness (false positives on idle), and a
  JS-visible server heartbeat (WS ping/pong cannot be observed from JS).
- 2026-06-29 (brick 6) The pong is wire msg type 5 (binary, `[1B type][8B ack=0]`) and decodes
  to null; the ping is `ControlMessage {type:"ping"}`. Why: keep the single binary
  server→client format; the pong carries no payload (its arrival is the whole signal), so
  decoding it to null — handled exactly like a dropped frame — keeps it out of the onMessage
  path without adding a ServerMessage union member that would ripple into every consumer.
- 2026-06-29 (brick 6) vibecli also reconnects on the window `online` event, alongside
  visibilitychange + pageshow. Why: a cheap, immediate trigger for a network handoff/restore
  (cellular↔wifi, tunnel reconnect); reconnectNow tears down any zombie and resume-by-index
  backfills what was missed.
- 2026-06-29 (brick 5) Brick 5 is a vibecli-only change (static/index.html + static-src/css +
  static-src/app.ts); vterm is untouched. Why: the contenteditable overload lived entirely in
  vibecli's DOM/CSS/input wiring — vterm's renderer (after brick 3) carries zero
  contenteditable/focus/selection assumptions (grep-confirmed). vibekit has its own input
  wiring, reconciled in brick 7. Rules out: a shared-library change for the element split.
- 2026-06-29 (brick 5) The `<textarea>` (#term-input) already existed (it was the iOS-keyboard
  element); brick 5 makes it the SOLE focus + keyboard + buffer + IME target and demotes
  #term-output to display-only (removed contenteditable, `-webkit-user-modify`, caret-color;
  kept user-select:text). focusTerminal() and the mouse-click path now focus the textarea; the
  output's per-element keydown and its entire beforeinput handler were deleted (typed text and
  paste now flow through the textarea's existing input/paste/composition listeners —
  composition.ts already brackets native paste). Why: the four-jobs-on-one-element overload is
  the shared cause of bugs 1/6/7; a humble textarea preserves the two properties the user named
  (keyboard summon; local buffer, via predict.ts + the outbox) without doing display or
  selection. Desktop typing now uses the same textarea input path iOS always used (verified
  live). Rules out: Shape 2 (a contenteditable input line) unless device IME testing forces it.
- 2026-06-29 (brick 5) The mouse-click focus path bails when a selection exists, so a
  drag-select is not collapsed by the focus grab. Why: with the textarea (not the element the
  selection lives in) as the focus target, unconditionally focusing on click would clobber a
  selection the user just made to copy. The touch pointerup path already had this guard.
- 2026-06-29 (brick 5) Re-enabled native `overflow-anchor: auto` on #term (was none). Why:
  §6.4 — the browser holds the read position on an insert-above for free. Safe because the
  absolute-index model only ever appends at the tail, so anchoring rarely engages and cannot
  fight the single scroll owner (the manual offsetTop math, Cause A, is already gone). Rules
  out: manual anchor compensation.
- 2026-06-29 (brick 5) showCtxMenu clamps within the viewport (add `.visible` to measure, then
  clamp left/top by offsetWidth/Height against innerWidth/Height with an 8px margin, all in one
  synchronous task so there is no flash). Why: bug 1d's off-screen callout. Verified live: a
  menu opened hard against the bottom-right corner lands fully inside the viewport.
- 2026-06-29 (brick 7) Wired the §8.2.2 resume eviction-gap affordance end to end (it was
  half-built — the store had `hasTrimmedHistory`/`noteResumeBounds` but nothing called them).
  connection.ts gains an `onResumeBounds(committed, oldest)` callback fired from the resumeAck
  branch; the consumer forwards it to `render.noteResumeBounds` → `store.noteResumeBounds`. The
  renderer shows a quiet "earlier output trimmed" marker — a non-data-abs first child of
  #term-output, so `insertRowInOrder` (which compares numeric data-abs) never places a row
  before it — when `store.hasTrimmedHistory()` (the client evicted its oldest lines at the
  5000-line cap, or the server's resumeAck reported it no longer retains history the client was
  missing). Why: completes the fifth of the user's "checks on reconnect"; a genuine,
  unrecoverable history trim becomes a visible affordance rather than a silent content
  discontinuity. Rules out: silently stitching across a gap.
- 2026-06-29 (brick 7) Guard audit conclusion: the §8.1 apply-line guards live in the store
  (brick 2 — valid index, stale-drop below everEvictedThrough, idempotent skip-if-equal,
  cap-evict) and the §8.2 resync guards are now all wired (epoch → onServerRestart; outbox/ack
  → applyAck clamp; window/cursor/modes → resume replay; index-alignment/trim → 8.2.2). No dead
  heuristics remained to remove — bricks 3/4 already deleted the live-zone trim, the per-frame
  caret hack, `suppressScroll`, and `isInUserScroll`.
- 2026-06-29 (brick 7) vibekit reconciled on its own `rebuild/terminal-viewer` branch: shell.ts
  wires `getHaveThrough` + `onResumeBounds` (resume-by-index parity for the shell), and
  `.shell-terminal` re-enables `overflow-anchor: auto` (was none, a stale live-zone-model
  setting). Verified by building vibekit against the LOCAL vterm working tree (a temporary
  go.work + a node_modules TS overlay): `go build ./...` and both tsgo passes (prod + tests)
  clean; node_modules then restored to the published package. vibekit uses only APIs that
  survived the rebuild; the removed `getScrollbackRowCount`/`suppressScroll`/`isInUserScroll`
  were never used by the shell (vibekit's own `scroll.ts` is a separate chat-scroll module).
  vibekit's shell input model (a focusable div + a helper textarea) never had vibecli's
  contenteditable overload, so brick 5's element split did not apply there. Rules out: a
  vibecli-style contenteditable rework of the vibekit shell.

## 10. Progress log

Append-only running state. This is the durable "where am I" across compaction. Newest at the
bottom.

- 2026-06-29 Audit complete (both halves), market research complete, live CDP harness proven
  against `vibecli-dev` (`192.168.1.77:9849`), root causes nailed for all seven bugs, design
  written (this document). No production code changed yet. Next action: brick 1 (line model +
  wire v2), starting with empirical Ink-fidelity capture of real kiro-cli frames to ground the
  setLine/screen/altLine split, then the Go `vt.Screen` absolute-index surface.
- 2026-06-29 BRICK 1 COMPLETE (branch `rebuild/terminal-viewer` in vterm). Wire v2 landed on
  both halves; the Go suite and the TS suite are both green.
  - Go: `scrollbackRing` is now absolute-index-aware (`Committed`/`OldestIndex`/`LinesFrom`,
    committed monotonic across eviction). `FlushFrameBuilder` diffs the window by ABSOLUTE
    INDEX (a pure scroll re-sends nothing) and carries `base` + `scrollFirstIdx` + `altActive`;
    forces a full repaint on alt-screen transitions. `terminal.go` tracks committed via the
    ring, resume replays by index with an eviction-gap signal and commits pending drain instead
    of discarding it. Encoders updated (`base` on screen, `first_index` on scroll,
    `committed`+`oldest_index` on resumeAck, alt-screen cursor-flag bit 3).
  - TS: decoder reads the new fields; `types.ts` updated; `ControlMessage.resume` uses
    `haveThrough` (sender temporarily sends -1, see decisions). `connection.ts` no longer
    imports `render`.
  - Tests added: ring absolute-index + eviction-gap + zero-capacity (Go); builder
    absolute-index integrity (simulated client reconstructs a gap-free, correctly-ordered
    buffer — the dup/gap-prevention proof, Go); wire-v2 field decode + back-compat tail (TS,
    `wire-v2.test.ts`). `WIRE_PROTOCOL.md` bumped to v2 with an absolute-indexing section.
  - Verification: `golangci-lint` 0 issues, `go test ./...` green; `eslint`/`prettier` clean,
    `vitest` 128/128 green. Not yet built into a running container (live integration verified
    at brick 3+ once the renderer consumes the new fields). NOT YET COMMITTED at time of this
    note; commit follows immediately.
  - Next: brick 2 (client absolute-index line store, pure + unit-tested), then brick 3
    (renderer consuming base/firstIndex + fixed-height window), at which point the live
    container loop + Ink-fidelity capture become worthwhile.

- 2026-06-29 BRICK 2 COMPLETE. New `web/src/store.ts`: `LineStore`, a pure
  (no-DOM) absolute-index model. One `Map<abs, runs>` capped at MAX_LINES (5000,
  injectable for tests), a window descriptor (base/height/cursor), an ephemeral alt grid,
  and oldest/highest/everEvictedThrough bounds. `applyScreen`/`applyScroll` funnel both wire
  shapes into one idempotent `applyLine` enforcing the section-8.1 guards (valid index,
  stale-drop below everEvictedThrough, idempotent skip-if-equal, cap eviction from the oldest
  end); alt frames route to the ephemeral grid and never touch the abs store. `forEachLine`
  iterates oldest..highest skipping holes (the renderer derives a trimmed-history gap from a
  jump in abs). `highestIndex()` is the resume `haveThrough`; `drainChanges()` hands the
  renderer dirty/evicted sets + window/alt/reset flags; `reset()` (server restart) clears all
  and re-allows index 0. `web/src/store.test.ts`: 10 tests — window/scroll apply, idempotent
  no-dup re-delivery, in-place update, cap eviction + stale-drop, hole-skipping, empty
  highestIndex, reset, alt routing + scroll-drop-during-alt, invalid-index reject.
  - Verification: tsgo prod + test typecheck clean, eslint + prettier clean, vitest 138/138
    (added store.test.ts + wire-v2.test.ts; fixed `base` field in 4 pre-existing render test
    helpers since ScreenMessage.base is now required). Pure logic, no container needed.
  - Next: brick 3 (renderer): rewrite render.ts to own a LineStore, consume base/firstIndex,
    render data-abs rows with a fixed-height window block (never trim trailing blanks), and
    apply drainChanges() with dirty-row updates. This is where the live container loop +
    Ink-fidelity capture (tasks 2/3) become worthwhile, so establish them at the start of
    brick 3.
- 2026-06-29 Ink-fidelity capture done (real kiro-cli on `vibecli-dev` via `/debug/raw`).
  Result: the VT core handles every sequence kiro-cli's Ink emits, so the highest-risk item
  (a wrong server buffer defeating a correct viewer) is retired. Sequences seen: heavy
  synchronized-output 2026 (handled via HoldFlush/ReleaseFlush), EL/CHA/CUU/CUD/CUP/ED/SGR/
  DECTCEM (all handled); ZERO scroll regions, IL/DL, or alt-screen in normal chat. Corrected
  section 5.3 and the 2026 decision (an earlier "unhandled" note was a flaky-tool artifact).
  Residual: the alt-screen ephemeral path is untested live (chat never triggers it); check it
  with a real editor session during brick 3.
- 2026-06-29 LIVE VERIFY LOOP ESTABLISHED + BRICK 1 VERIFIED LIVE against real kiro-cli.
  Loop (vibecli `scripts/`, committed on vibecli branch `rebuild/terminal-viewer`):
  `dev-build.sh` builds vibecli against the local `../vterm` (go.work + overlaid TS + cached
  Monaspace font) into `vibecli-dev-bin`; `dev-deploy.sh` swaps it into the `vibecli-dev`
  container (`docker cp` + restart, no image rebuild, no CI); `cdp-verify.cjs` drives the
  Chromium sidecar and dumps a DOM/scroll snapshot. Result: the v2 binary renders the real
  kiro-cli welcome TUI correctly — 31 rows, banner + status line (`claude-opus-4.8 · Max`) +
  input prompt, ZERO console errors, `maxConsecutiveDup=1` (no duplicate rows), loading
  overlay cleared. Brick 1's wire v2 is validated end-to-end with real kiro-cli, not just unit
  tests. Two harness learnings (also in section 12): (1) a CDP-opened sidecar tab is HIDDEN, so
  Chromium pauses requestAnimationFrame and the rAF-batched renderer paints nothing — must call
  `Page.bringToFront` + `Emulation.setFocusEmulationEnabled{enabled:true}` or the DOM stays
  empty while the server flushes correctly; (2) the dev build needs the Monaspace font present
  or `document.fonts.load` never resolves and the client never sends the kiro-cli-starting
  resize. Also fixed: the first vibecli dev-loop commit landed on `main` by mistake and was
  moved to the `rebuild/terminal-viewer` branch (main reset to origin/main; nothing lost).
- 2026-06-29 BRICK 3 COMPLETE (renderer rewritten to the store-backed, absolute-index DOM
  model) and VERIFIED LIVE against real kiro-cli. `render.ts` now owns a `LineStore` and
  reflects it: each line is a `div.term-row` with `data-abs`, rows live in one
  absolute-ordered container, and a single rAF flush drains `store.drainChanges()` to
  evict/upsert rows. Reused verbatim from the old renderer: `buildRowSpans` (cell→DOM, wide
  chars, OSC 8 + autolink), the width-measurement cache, font metrics, `computeSize`, cursor
  blink, reverse-video. Removed: the `allRows`/`liveCount` live-zone model, `ensureLiveZone`'s
  trailing-blank trim (the bug-3 oscillation source), the `flushAll`/`flushScreenInner`
  scroll+selection entanglement, and the per-frame `selectAllChildren+collapseToEnd` caret
  hack (the bug-1 selection killer) — both gone. `handleScreen`/`handleScroll` now just feed
  the store. `getCursorPx`/`setPredictedCursor` derive position from the cursor row's actual
  DOM offset. API change: `getScrollbackRowCount()` (DOM-count, now meaningless) removed,
  replaced by `getHighestIndex()` (the resume `haveThrough`); `resetScreen`/`resetScrollback`
  both map to `store.reset()`. A basic alt-screen path renders the ephemeral grid and rebuilds
  from the store on exit (untested live — chat never triggers alt; needs an editor session).
  Live result (cdp-verify against real kiro-cli): rowCount=79 (the FULL fixed-height window,
  not trimmed to the 22 non-empty lines — bug-3 fix confirmed), maxConsecutiveDup=1 (no dup),
  zero console errors, scrollHeight==clientHeight (no oscillation). Tests: all 144 vitest green
  (existing render/hyperlink/wide-render/pipeline tests pass unchanged against the new model,
  proving DOM compatibility), plus new `render-store.test.ts` (6) pinning data-abs tagging,
  fixed-height-no-trim, history+window ordering, re-delivery dedup, in-place update, cursor
  span, and full-reset wipe. Brick 3 kills bug 3 (oscillation) and the core of bug 1 (selection
  no longer destroyed every frame); the rest of bug 1 (touch/menu) is brick 5.
- 2026-06-29 BRICK 4 COMPLETE (scroll controller) + VERIFIED LIVE with a streaming fixture.
  `scroll.ts` rewritten to a single owner of `scrollTop` with one piece of state, `following`,
  derived purely from scroll position (24px bottom tolerance). No suppress window, no debounce,
  no 60-second touch timer — the heuristic soup (Cause A/E) is gone. `stickToBottom()` (renderer
  calls it once per flush) pins to the bottom only while following; `scrollToBottom()` re-engages
  following; `isUserScrolledUp()` = `!following`. Removed `suppressScroll`/`isInUserScroll`
  (clean break); updated the two consumers — render.ts `stickToBottomIfFollowing` now just calls
  `scroll.stickToBottom()`, and vibecli's `viewport.ts` dropped the `suppressScroll` call (its
  settle→`scrollToBottom` already re-pins, so the transient keyboard-slide flip self-corrects).
  Native `overflow-anchor` re-enable is a vibecli CSS change deferred to brick 5. Tests: new
  `scroll.test.ts` (6, happy-dom with overridden scroll geometry: follow/hold toggle, tolerance,
  stick-only-when-following, no-yank-when-holding, jump re-engages); full suite 150 green.
  Live (emitter fixture bursting 120 lines then 1/0.4s, on a second vibecli instance :9850):
  initiallyFollowing dist=0; scrolled up to 930 then 6 lines arrived (rows 182→188, scrollHeight
  3102→3204) and scrollTop STAYED 930 — held, no yank (bug 4 fixed); jump-to-bottom re-followed
  (dist=0). Bricks 3+4 together resolve bugs 3 and 4. Fixture: `vibecli/scripts/emit-fixture.sh`
  (ignores its `chat` arg, emits forever) run as a vibecli `KIRO_CLI_PATH` from a non-noexec dir
  (TrueNAS `/tmp` is noexec — run the dev binary from `~`, not `/tmp`).

- 2026-06-29 BRICK 6 COMPLETE (connection state machine; bug 2) + VERIFIED LIVE end-to-end.
  This is the resilience brick; it was done before brick 5 because it is CDP-verifiable without
  a real device (brick 5's touch work needs the iPhone). Three coordinated changes plus a
  defense-in-depth probe, all in vterm's shared `connection.ts` so vibecli AND vibekit inherit
  them:
  - `reconnectNow()` is now UNCONDITIONAL. It no longer early-returns on `connState ===
    "connected"`; it aborts+closes whatever socket exists and reconnects. The early-return was
    bug 2's smoking gun (Cause D): on iOS wake the socket reads OPEN but is a zombie, so the old
    code skipped the reconnect, never resumed, and sleep-window output stayed missing until a
    manual refresh. This is the design's `ensureFresh()` (kept the name reconnectNow; see the
    decisions log).
  - Resume now carries `haveThrough` = the highest absolute line index the client holds
    (`render.getHighestIndex` via a new `getHaveThrough` callback, wired in vibecli `app.ts`),
    replacing brick 1's temporary -1. The server (brick 1 `handleResume`) replays exactly lines
    > haveThrough, so sleep-window output backfills on wake; idempotent absolute-index apply
    means re-delivery never duplicates (also keeps bug 5 fixed across reconnects).
  - Client-side liveness probe (defense-in-depth for a silently half-open socket that fires no
    wake event). A heartbeat evaluates every 5s; after 10s of inbound silence on a VISIBLE tab
    it sends `{type:"ping"}`; the server (`handleControl`→`handlePing`) echoes a binary pong
    (wire msg type 5, `encodePongMsg`). ANY inbound frame (the pong or normal output) refreshes
    the activity clock (`markActivity` runs at the top of the message listener, before decode);
    if a probe draws no reply within a 7s grace the socket is declared stale and reconnectNow()
    fires. The probe is what tells "idle but alive" from "dead", so a quiet terminal never
    reconnect-flaps. Hidden tabs are skipped (wake events own them). Wire v2 grew by:
    `ControlMessage |{type:"ping"}`; `MSG_PONG=5` decoding to null (no payload; arrival is the
    signal). vibecli also wires the `online` event to reconnectNow alongside
    visibilitychange+pageshow.
  - Tests: TS `connection.test.ts` +5 (reconnectNow tears down a healthy socket and opens a
    fresh one; resume carries the reported haveThrough; fallback -1 when unwired; probe →
    reconnect on no pong; a pong/any-frame keeps the socket and stays out of onMessage). Go
    `terminal_test.go` +1 (a ping elicits a pong frame, full round-trip via dialHandler, passes
    `-race`). Full gate green: `go test -race ./...` + `golangci-lint` 0 issues; tsgo prod+test,
    eslint, prettier, vitest 155/155.
  - LIVE VERIFY (new `vibecli/scripts/cdp-bug2.cjs`): the :9850 emitter instance streams lines
    continuously. The harness wraps `window.WebSocket` (injected via
    `Page.addScriptToEvaluateOnNewDocument` — no production hook) so it can truly SEVER the
    socket and block reconnects on demand. KEY HARNESS LEARNING: CDP
    `Network.emulateNetworkConditions{offline:true}` does NOT close an already-established
    WebSocket (the first attempt showed the client still receiving during the "outage"), so a
    faithful outage needs the socket-wrapper approach, not Network offline. Result: before
    outage maxAbs=585 (contiguous, 0 dup); DURING outage maxAbs stayed 585 (client genuinely
    dark — the sever worked); after a pageshow-driven reconnect maxAbs=620 with the missed lines
    586..620 ALL backfilled, abs indices contiguous 0..620, 0 duplicates, no console errors. Bug
    2 is fixed at the root (resume-by-absolute-index + unconditional wake teardown), proven live,
    not patched.
  - Next: brick 5 (input/keyboard/selection separation; bugs 1, 6, 7) — needs the iPhone for
    final touch validation, so it is the natural next step now that the CDP-verifiable bricks
    (1–4, 6) are done. Then brick 7 (guard hardening §8 + vibekit reconcile, including any
    rename of reconnectNow→ensureFresh).

- 2026-06-29 BRICK 5 COMPLETE (input / keyboard / selection separation; bugs 1, 6, 7) +
  structurally VERIFIED LIVE. vibecli-only (vterm untouched — its renderer carries no
  contenteditable/focus assumptions). The `<textarea>` already existed for the iOS keyboard;
  the change makes it the SINGLE focus/keyboard/buffer/IME target and demotes #term-output to
  display-only:
  - `static/index.html`: #term-output lost `contenteditable` + the editable-only attrs
    (autocapitalize/autocomplete/autocorrect/spellcheck/aria-readonly/tabindex); it is now a
    plain role=log display surface.
  - `static-src/css/02-app.css`: .term-output dropped caret-color:transparent and
    `-webkit-user-modify` (editable-only), kept user-select:text + position:relative; .term
    re-enabled `overflow-anchor: auto` (was none).
  - `static-src/app.ts`: focusTerminal() focuses the textarea (was the output); deleted the
    output's keydown listener and its entire beforeinput handler (typed text/paste now flow
    through the textarea's existing input/paste/composition listeners — composition.ts already
    brackets native paste); the mouse-click path focuses the textarea but bails when a
    selection exists (so a drag-select is not collapsed); showCtxMenu clamps within the
    viewport.
  - Why this kills the bugs: the editable element is no longer the scroll content, so the first
    touch-drag scrolls instead of placing a caret (bug 6), and #term is the full-viewport tap
    surface so a tap on a sparse screen still summons the keyboard (bug 7); #term-output is not
    re-rendered as editable and the per-frame caret hack is already gone (brick 3), so a
    selection survives a redraw (bug 1); the menu is clamped (bug 1d).
  - LIVE VERIFY (new `scripts/cdp-bug5.cjs`) against BOTH the :9850 emitter and real kiro-cli
    (:9849): 8/8 PASS — #term-output isContentEditable=false; document.activeElement is the
    textarea (not the output); #term overflow-anchor=auto; #term-output user-select=text; a
    context menu opened hard against the bottom-right corner lands fully inside the viewport;
    and a 220-char selection over a committed row survived 4s of streaming frames unchanged
    (bug 1 core). No console errors against the real TUI with the new DOM. Desktop typing
    confirmed live (throwaway probe): with the textarea focused, an inserted marker
    round-tripped through kiro-cli and rendered back in the DOM.
  - DEVICE-ONLY residuals (the desktop sidecar cannot reproduce iOS WebKit; see §11): the first
    touch-drag actually scrolling (bug 6), a tap actually summoning the iOS keyboard (bug 7),
    the native long-press selection callout, and whether a 1-line textarea suffices for IME
    (Shape 2 fallback). CDP synthetic touch could not confirm tap→focus (it does not emit
    pointerType=touch reliably), but the wiring is unchanged from the pre-existing touch path
    and the focus model is structurally proven.
  - Next: brick 7 (guard hardening §8 + vibekit reconcile). Brick 5's touch behaviors await the
    user's iPhone for final sign-off.

- 2026-06-29 BRICK 7 COMPLETE (guard hardening §8 + vibekit reconcile). All seven bricks are
  now laid; the rebuild is structurally complete.
  - §8.2.2 resume eviction-gap affordance wired end to end (it was half-built — the store had
    `hasTrimmedHistory`/`noteResumeBounds` but nothing called them). connection.ts gains an
    `onResumeBounds(committed, oldest)` callback fired from the resumeAck branch; vibecli
    `app.ts` and vibekit `shell.ts` forward it to `render.noteResumeBounds` → store. render.ts
    shows a quiet "earlier output trimmed" marker as a non-data-abs first child of #term-output
    when `store.hasTrimmedHistory()` (client hit the 5000-line cap, or the server reported it
    evicted history the client was missing on resume). CSS: vibecli `02-app.css` +
    vibekit `21-shell-panel.css`.
  - Guard audit: §8.1 apply-line guards confirmed in the store (brick 2); §8.2 resync guards now
    all wired (epoch, outbox/ack clamp, window/cursor/modes via replay, and now the 8.2.2 trim).
    No dead heuristics left to delete (bricks 3/4 already removed the live-zone trim, the
    per-frame caret hack, `suppressScroll`, `isInUserScroll`).
  - vibekit reconciled on its own `rebuild/terminal-viewer` branch (commit ee68fd3 + the
    onResumeBounds/CSS follow-up): shell.ts wires `getHaveThrough` + `onResumeBounds`;
    `.shell-terminal` overflow-anchor none→auto. VERIFIED by building vibekit against the LOCAL
    vterm working tree (temporary go.work + node_modules TS overlay): `go build ./...` clean,
    tsgo prod + tests clean. node_modules restored to the published package afterward; like
    vibecli's branch it references unpublished vterm APIs, so it is staged for the lockstep
    vterm publish (not green against the pinned v1.1.8 by design).
  - Tests: vterm +4 — store (client-evict trim; server-gap trim via noteResumeBounds;
    invalid-bounds ignored) and render (marker appears as first child with rows ordered after,
    then clears). Full vterm gate green: `go test -race` + golangci-lint 0; tsgo prod+test,
    eslint, prettier, vitest 159.
  - Live re-verify after brick 7 (no regression): cdp-bug2 PASS (backfill contiguous, 0 dup,
    and no spurious marker — contiguous from abs 0), cdp-bug5 PASS (8/8 structural).
  - OBSERVATION (not one of the seven bugs; see §11): a synthetic 1500-line instant burst (an
    emit-burst fixture, larger than the 1000-line server ring) overwhelmed the SHARED CDP
    sidecar browser's rendering and crashed its debug endpoint (recovered by restarting the
    chromium container). The server delivered correctly and a fresh client gets the ring via
    resume replay; the issue was the constrained desktop test browser rendering ~1426 new DOM
    rows in a single rAF. kiro-cli's Ink output is incremental, not a 1500-line instant dump,
    so this is outside the rebuild's bug set — but a `cat bigfile` burst is worth a future look
    (chunked rendering / a cap on rows built per frame). The trim marker's live trigger needs
    server eviction (a heavy burst), which is exactly what destabilized the sidecar, so the
    marker is proven by unit tests end to end instead of live.
  - State: bricks 1-7 done. Remaining is not code — it is the §11 device-validation checklist
    on a real iPhone (touch select/scroll/keyboard for bugs 1/6/7, on-device bug-2/3/4
    confirmation, IME), then the coordinated vterm publish + consumer dep-bumps (vibecli and
    vibekit both have `rebuild/terminal-viewer` branches staged against the unpublished vterm).

## 11. Open questions and risks

- Ink redraw fidelity (section 5.3). Verify scroll-region and erase-display handling against
  real kiro-cli frames before trusting the buffer. Highest risk to the whole effort, because a
  wrong buffer defeats a correct viewer.
- Index width on the wire: uint64 is simplest and never wraps in practice; a session-relative
  uint32 halves header size but needs wrap handling. Decide in brick 1 with a size/complexity
  tradeoff, default to uint64 unless the frame budget demands otherwise.
- IME on a 1-line textarea (section 6.3). Validate live; Shape 2 fallback ready.
- iPhone-only behaviors the desktop sidecar cannot fully reproduce: momentum-scroll feel, the
  native selection callout, BFCache restore, soft-keyboard insets. These need a real-device
  pass at the end; the desktop harness covers structure, scroll math, dedup, and resume.
- Predictive echo (`predict.ts`) and the local typing buffer interaction with the new input
  element: keep predictive echo, re-point it at the textarea-driven input. Verify it does not
  fight the server cursor under the new screen model.
- Large instant bursts (`cat bigfile`): a 1500-line instant burst (a synthetic stress fixture,
  larger than the 1000-line server ring) overwhelmed the shared desktop CDP sidecar's rendering
  during brick 7 (it built ~1426 new DOM rows in one rAF and crashed the browser's debug port).
  The wire side is fine — the server commits and delivers correctly, and a client connecting
  after the burst gets the ring via resume replay — so this is a client-render throughput
  concern, not a protocol bug, and it is outside the seven reported bugs (kiro-cli's Ink output
  is incremental, never a 1500-line instant dump). Worth a future look if `cat bigfile` matters:
  cap rows built per rAF (render a budget per frame, continue next frame) or coalesce a very
  large dirty set. Not pursued now to avoid destabilizing the shared sidecar.

### Device-validation checklist (real iPhone, after brick 5)

Everything CDP-observable for bricks 1–6 is verified (see the progress log). These remain
because desktop Chromium cannot reproduce iOS WebKit; they are the morning hands-on pass on a
real device against the `vibecli-dev` build:

- Bug 1: long-press a streaming region and a static region; selection holds, the callout/our
  menu appears on-screen, Copy works. Paste via the iOS callout inserts (bracketed).
- Bug 6: the FIRST touch-drag after load scrolls (no need to tap/select first).
- Bug 7: a tap anywhere — including a near-empty first screen — summons the keyboard.
- Bugs 3/4 on-device: streaming does not jump; scrolled-up reading holds while new text
  arrives; jump-to-bottom re-follows.
- Bug 2 on-device: lock the phone mid-stream, unlock, confirm the gap backfills with no
  refresh and no duplicates.
- IME (§6.3 open question): compose CJK/dictation; confirm the candidate popup tracks the
  cursor with the 1-line textarea, else adopt Shape 2.

## 12. Experiment harness

How to observe and verify live, so the rebuild is grounded in real DOM behavior, not just code
reasoning.

CDP sidecar (shared Chromium on Borgcube, see the chromium-sidecar steering doc):

- CDP endpoint: `http://192.168.1.77:9222`. Tab list at `/json`. Open a tab with
  `PUT /json/new?<url>`; drive it over the returned `webSocketDebuggerUrl`; close with
  `/json/close/<id>`. Node 22 has a global `WebSocket` and `fetch`, so a zero-dependency CDP
  driver works (validated this session). If using puppeteer, `connect({ browserURL,
  defaultViewport: null })` and `disconnect()` (not `close()`); clear any leftover
  `Emulation` device-metrics override after attach.
- The sidecar is desktop Chromium. Good for structure, scroll math, dedup, resume, and touch
  emulation. Not a substitute for a real iPhone on WebKit-specific behavior.

vibecli targets on Borgcube:

- `vibecli-dev`: host `192.168.1.77:9849` -> container `9848`. The experiment target. Rebuild
  its image locally and redeploy here; do not go through GitHub Actions.
- `vibecli` (prod): host `192.168.1.77:9848`. Leave alone except for final validation.

Local build/deploy loop (admin override, off the permanent record): build the vibecli image on
Borgcube with `docker build`, retag, and restart the `vibecli-dev` container against the new
image. Exact commands are recorded in the progress log as the loop is established in brick 1.

Per-brick verification asserts the specific failure is gone: `scrollHeight` stable during an
in-place redraw (bug 3), no upward yank while Holding (bug 4), no duplicate `data-abs` after a
forced reconnect (bug 5), content present after a simulated sleep/wake (bug 2), selection
surviving a stream (bug 1), first touch-drag scrolling (bug 6), tap-anywhere summoning the
keyboard (bug 7).

### Proven loop (established brick 1, 2026-06-29)

The loop is implemented as three scripts on the vibecli `rebuild/terminal-viewer` branch and
was used to validate brick 1 against real kiro-cli:

1. `vibecli/scripts/dev-build.sh` — builds vibecli against the local `../vterm` working tree.
   Writes a `go.work` (`use .` + `use ../vterm`), overlays `vterm/web/src/*.ts` onto
   `static-src/node_modules/@cplieger/vterm/src`, runs the two tsgo passes (app + lib), fetches
   the Monaspace Nerd Font (cached in `~/.cache/vibecli-fonts`), concatenates the CSS, and
   `go build`s `vibecli-dev-bin` (CGO off; Constellation's linux/amd64 matches the container).
2. `vibecli/scripts/dev-deploy.sh` — `scp` the binary to Borgcube, `sudo docker cp` it to
   `vibecli-dev:/app/vibecli`, `sudo docker restart vibecli-dev`, poll `/api/health`. No image
   rebuild, no GitHub Actions. Prod `vibecli` (9848) is untouched; `vibecli-dev` is 9849.
3. `vibecli/scripts/cdp-verify.cjs` — opens vibecli-dev in the sidecar, captures console
   errors/exceptions, waits for kiro-cli to render, dumps a DOM/scroll snapshot
   (`rowCount`, `nonEmptyLines`, `maxConsecutiveDup`, scroll metrics, first/last lines).

Two non-obvious requirements, both learned the hard way during brick 1:

- A CDP-opened sidecar tab is a BACKGROUND tab, and Chromium pauses `requestAnimationFrame`
  for hidden tabs. The renderer batches via rAF, so the DOM stays empty even though the server
  is flushing frames correctly and there are no JS errors. Force the page active with
  `Page.bringToFront` + `Emulation.setFocusEmulationEnabled({enabled:true})`. The probe
  reports `visibilityState`/`hasFocus`/`rafFired` so this failure mode is obvious next time.
- The dev build must include the Monaspace font. The client gates its first kiro-cli-starting
  `resize` on `document.fonts.load('14px "MonaspiceNe NFM"')` resolving; with the font absent
  that never resolves, no resize is sent, and kiro-cli never starts (blank terminal).

`/debug/raw` (raw PTY ring) and `/debug/screen` (server screen dump) on the vibecli port are
invaluable for separating server-side from client-side issues: during brick 1, `/debug/screen`
showed the welcome banner present server-side while the browser showed nothing, which isolated
the problem to the client (the rAF/visibility issue above), not the wire.
