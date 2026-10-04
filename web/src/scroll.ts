// Scroll controller: the single owner of one container's scrollTop and of the
// follow state. Every movement is classified by its delta from `expectedTop`,
// the offset the library last wrote or observed, so a user move that shares one
// coalesced scroll event with the library's own write is still seen. Input
// intent on the container's window disengages before any event arrives, and is
// undone when no movement follows; while a touch or scrollbar press is down the
// bottom pin holds off.
const BOTTOM_TOLERANCE_PX = 24;
// Absorbs fractional-layout rounding on a clamp or an echo; far below any real
// one-frame user scroll increment.
const CLAMP_EPSILON_PX = 1;
// A finger travelling this far down is pulling the content: the reader is
// scrolling up. Android's TOUCH_SLOP (8dp), https://cs.android.com/android/platform/superproject/+/main:frameworks/base/core/java/android/view/ViewConfiguration.java
const TOUCH_PULL_PX = 8;
// Releases a pin hold where `scrollend` never fires: Safari shipped it in 26.2,
// https://developer.mozilla.org/docs/Web/API/Element/scrollend_event#browser_compatibility
const RELEASE_QUIET_MS = 200;

/** What `createScrollController` binds to and notifies. */
export interface ScrollControllerOptions {
  /** Element whose scroll position is observed and owned. */
  scrollEl: HTMLElement;
  /** Fired on a follow/hold toggle; the argument is true when the user has scrolled up. */
  onUserScrollChange?: (scrolledUp: boolean) => void;
  /** Fired on every scroll event that reflects a real position change. */
  onScrollPosition?: () => void;
}

/** The single owner of one scroll container's `scrollTop` and follow state. */
export interface ScrollController {
  /** Announce that the caller's own mutation just REMOVED content; pass the offset read before it. */
  noteContentShrink(scrollTopBefore: number): void;
  /** Move the offset back inside the range after an announced shrink left it past the end. */
  reconcileScrollRange(): void;
  /** Pin the viewport to the bottom iff the user is following. */
  stickToBottom(): void;
  /** Force scroll to the bottom and re-engage following. */
  scrollToBottom(): void;
  /** Whether the user has scrolled away from the bottom (auto-follow disengaged). */
  isUserScrolledUp(): boolean;
  /** The viewport's current scroll offset; 0 once disposed. */
  currentScrollTop(): number;
  /** Shift the viewport by a content-height change ABOVE the reading position; a no-op while following. */
  adjustForContentShift(deltaPx: number): void;
  /** Restore a saved view: the offset and the follow state, together and explicitly. */
  restoreView(view: { top: number; following: boolean }): void;
  /** Removes every listener and drops the element and callbacks; every method is a no-op after. */
  dispose(): void;
}

/**
 * Creates the scroll controller for `opts.scrollEl`, following from
 * construction. Throws, holding nothing, when the listener cannot be attached.
 */
export function createScrollController(opts: ScrollControllerOptions): ScrollController {
  let scrollEl: HTMLElement | null = opts.scrollEl;
  const win = opts.scrollEl.ownerDocument.defaultView;
  let following = true;
  let expectedTop = opts.scrollEl.scrollTop;
  // Armed by noteContentShrink when the caller's own row removal moved the
  // position; consumed by the next classification, event or settle alike.
  let shrinkArmed = false;
  // Armed by noteContentShrink when the removal left the offset PAST the end
  // of the content; consumed by reconcileScrollRange in the same pass.
  // Independent of shrinkArmed: a partial reconciliation sets both.
  let rangeCorrectionOwed = false;
  // The echo of an anchor correction, a range correction or a restore notifies
  // nothing: a paged-in prepend corrects through adjustForContentShift, and
  // notifying for it would make every prepend a fetch trigger.
  let echoSilent = false;
  // A settle found user movement inside a write; the next event reports it.
  let positionOwed = false;
  // The pin hold: a press is down, or its momentum has not settled yet.
  let pressHeld = false;
  let momentumPending = false;
  let scrolledSincePress = false;
  let pinOwed = false;
  // Intent turned follow off; no movement has confirmed it yet.
  let intentUnconfirmed = false;
  let touchStartY = 0;
  let quietTimer: number | undefined;
  // A touch's events target its start node
  // (https://w3c.github.io/touch-events/#the-touchend-event) and the renderer
  // rebuilds rows in place; a detached node's events never reach the window
  // (https://dom.spec.whatwg.org/#concept-event-dispatch), so the container's
  // descendants on the touch's path listen too. The container itself never does.
  const touchTargets = new Set<EventTarget>();
  let onFollowChange: ((scrolledUp: boolean) => void) | null = opts.onUserScrollChange ?? null;
  let onPosition: (() => void) | null = opts.onScrollPosition ?? null;

  function distanceFromBottom(): number {
    if (!scrollEl) {
      return 0;
    }
    return scrollEl.scrollHeight - scrollEl.scrollTop - scrollEl.clientHeight;
  }

  // The largest offset the container can hold. Every write that means "the
  // bottom" targets this rather than `scrollHeight`: writing the maximum needs
  // no clamp to be correct on a container that leaves offsets out of range.
  function bottomOffset(): number {
    if (!scrollEl) {
      return 0;
    }
    return Math.max(0, scrollEl.scrollHeight - scrollEl.clientHeight);
  }

  function atBottom(): boolean {
    return distanceFromBottom() <= BOTTOM_TOLERANCE_PX;
  }

  function setFollowing(next: boolean): void {
    if (next === following) {
      return;
    }
    following = next;
    if (onFollowChange) {
      onFollowChange(!following);
    }
  }

  // Returns whether `top` is a real movement. An echo leaves the baseline where
  // it is, so sub-epsilon steps accumulate into a movement rather than vanish.
  function classify(top: number): boolean {
    const delta = top - expectedTop;
    const wasShrink = shrinkArmed;
    shrinkArmed = false;
    if (Math.abs(delta) <= CLAMP_EPSILON_PX) {
      return false;
    }
    expectedTop = top;
    // Only a movement that leaves a gap below confirms intent: a clamp landing
    // at the tail (a viewport grow) is the browser's, not the reader's.
    if (!wasShrink && distanceFromBottom() > CLAMP_EPSILON_PX) {
      intentUnconfirmed = false;
    }
    if (delta < 0) {
      // Upward: neither a shrink clamp nor a user may ENGAGE follow this way, so
      // a clamp landing at the bottom under a holding reader keeps them holding.
      if (!wasShrink && distanceFromBottom() > CLAMP_EPSILON_PX) {
        setFollowing(false);
      }
      return true;
    }
    // Downward only engages: a pin hold lets output grow below a following
    // reader, and a move toward the tail that falls short is not leaving it.
    if (atBottom()) {
      setFollowing(true);
    }
    return true;
  }

  // Classifies a movement the page can see before its event: the order "user
  // moves, the library writes, the event dispatches" must not erase the move.
  function settle(): void {
    if (scrollEl && classify(scrollEl.scrollTop)) {
      positionOwed = true;
      if (pressHeld) {
        scrolledSincePress = true;
      }
    }
  }

  // Every library write ends here, so the baseline is the post-clamp offset.
  function write(next: number, silent: boolean): void {
    if (!scrollEl) {
      return;
    }
    const before = scrollEl.scrollTop;
    scrollEl.scrollTop = next;
    expectedTop = scrollEl.scrollTop;
    if (expectedTop !== before) {
      echoSilent = silent;
    }
  }

  const onScroll = (): void => {
    if (!scrollEl) {
      return;
    }
    const top = scrollEl.scrollTop;
    const echo = Math.abs(top - expectedTop) <= CLAMP_EPSILON_PX;
    const silent = echoSilent && echo && !positionOwed;
    echoSilent = false;
    positionOwed = false;
    // Reported before the follow decision and apart from it: a reader browsing
    // history never toggles follow, and idle browsing is when paging must work.
    if (!silent && onPosition) {
      onPosition();
    }
    if (classify(top) && pressHeld) {
      scrolledSincePress = true;
    }
    if (momentumPending) {
      armQuietTimer();
    }
  };

  function inside(target: EventTarget | null): boolean {
    return (
      scrollEl !== null &&
      target !== null &&
      typeof (target as Partial<Node>).nodeType === "number" &&
      scrollEl.contains(target as Node)
    );
  }

  // Intent is provisional: it opens the quiet window a momentum scroll gets, and
  // the release restores follow unless a movement confirmed it meanwhile, so a
  // touch slop or a key that scrolled some other element cannot strand the reader.
  function disengageForIntent(): void {
    if (!following || !scrollEl || scrollEl.scrollTop <= CLAMP_EPSILON_PX) {
      return;
    }
    setFollowing(false);
    intentUnconfirmed = true;
    if (!pressHeld) {
      momentumPending = true;
      armQuietTimer();
    }
  }

  function clearQuietTimer(): void {
    if (quietTimer !== undefined && win) {
      win.clearTimeout(quietTimer);
    }
    quietTimer = undefined;
  }

  function armQuietTimer(): void {
    clearQuietTimer();
    if (win) {
      quietTimer = win.setTimeout(releaseHold, RELEASE_QUIET_MS);
    }
  }

  function releaseHold(): void {
    clearQuietTimer();
    momentumPending = false;
    if (intentUnconfirmed) {
      intentUnconfirmed = false;
      setFollowing(true);
      pinOwed = true;
    }
    if (pinOwed) {
      pinOwed = false;
      stickToBottom();
    }
  }

  // A press that scrolled nothing (a tap) releases at once; one that scrolled,
  // or whose intent has not shown its scroll yet, waits for the quiet window.
  function endPress(): void {
    if (!pressHeld) {
      return;
    }
    pressHeld = false;
    if (scrolledSincePress || intentUnconfirmed) {
      momentumPending = true;
      armQuietTimer();
    } else {
      releaseHold();
    }
  }

  function beginPress(): void {
    clearQuietTimer();
    pressHeld = true;
    momentumPending = false;
    scrolledSincePress = false;
  }

  function releaseTouchTargets(): void {
    for (const t of touchTargets) {
      t.removeEventListener("touchmove", onDetachedTouch);
      t.removeEventListener("touchend", onDetachedTouch);
      t.removeEventListener("touchcancel", onDetachedTouch);
    }
    touchTargets.clear();
  }

  // Window listeners run in the BUBBLE phase, after every element and document
  // listener, so a gesture the page consumed (mouse tracking cancels the wheel,
  // a mapped key is cancelled and sent) is visible as `defaultPrevented`.
  function onWheel(e: WheelEvent): void {
    if (e.deltaY < 0 && !e.ctrlKey && !e.defaultPrevented && inside(e.target)) {
      disengageForIntent();
    }
  }

  function onKeydown(e: KeyboardEvent): void {
    if (e.key !== "PageUp" && e.key !== "Home") {
      return;
    }
    if (e.defaultPrevented) {
      return;
    }
    const doc = scrollEl?.ownerDocument;
    if (e.target === doc?.body || inside(e.target)) {
      disengageForIntent();
    }
  }

  function onTouchStart(e: TouchEvent): void {
    if (e.defaultPrevented || !inside(e.target)) {
      return;
    }
    for (const node of e.composedPath()) {
      if (node === scrollEl) {
        break;
      }
      if (!touchTargets.has(node)) {
        node.addEventListener("touchmove", onDetachedTouch, { passive: true });
        node.addEventListener("touchend", onDetachedTouch, { passive: true });
        node.addEventListener("touchcancel", onDetachedTouch, { passive: true });
        touchTargets.add(node);
      }
    }
    if (e.touches.length === 1) {
      touchStartY = e.touches[0]?.clientY ?? 0;
      beginPress();
    }
  }

  function notePull(e: TouchEvent): void {
    const y = e.touches[0]?.clientY;
    if (pressHeld && y !== undefined && y - touchStartY >= TOUCH_PULL_PX) {
      disengageForIntent();
    }
  }

  function onTouchMove(e: TouchEvent): void {
    if (!e.defaultPrevented && inside(e.target)) {
      notePull(e);
    }
  }

  // Acts only once the touched node has left the container; an attached touch
  // stays on the window path. A move is judged at the last node of the detached
  // path, after every listener the page had on that path when the touch began,
  // so a cancel is visible; an end at the target, so no ancestor's listener can
  // stop the release.
  function onDetachedTouch(e: Event): void {
    if (inside(e.target)) {
      return;
    }
    if (e.type !== "touchmove") {
      if (e.currentTarget === e.target) {
        onTouchEnd(e as TouchEvent);
      }
      return;
    }
    let last: EventTarget | undefined;
    for (const node of e.composedPath()) {
      if (touchTargets.has(node)) {
        last = node;
      }
    }
    if (e.currentTarget === last && !e.defaultPrevented) {
      notePull(e as TouchEvent);
    }
  }

  function onTouchEnd(e: TouchEvent): void {
    if (e.touches.length > 0) {
      return;
    }
    releaseTouchTargets();
    endPress();
  }

  function onPointerDown(e: PointerEvent): void {
    if (
      scrollEl !== null &&
      e.pointerType === "mouse" &&
      e.target === scrollEl &&
      !e.defaultPrevented &&
      e.offsetX >= scrollEl.clientWidth
    ) {
      beginPress();
    }
  }

  function onPointerUp(e: PointerEvent): void {
    if (e.pointerType === "mouse") {
      endPress();
    }
  }

  // An element's `scrollend` does not bubble, so it is caught on the way down:
  // https://drafts.csswg.org/cssom-view/#scrolling-events
  function onScrollEnd(e: Event): void {
    if (e.target === scrollEl && momentumPending) {
      releaseHold();
    }
  }

  // A hidden page receives no further input, so a press whose end never
  // arrives must not keep holding the pin until the next touch. Captured, so a
  // page listener that stops the document's event cannot hide it.
  function onVisibilityChange(): void {
    if (scrollEl?.ownerDocument.visibilityState === "hidden" && (pressHeld || momentumPending)) {
      releaseTouchTargets();
      pressHeld = false;
      releaseHold();
    }
  }

  const windowListeners: readonly (readonly [
    string,
    (e: never) => void,
    AddEventListenerOptions,
  ])[] = [
    ["wheel", onWheel, { passive: true }],
    ["keydown", onKeydown, { passive: true }],
    ["touchstart", onTouchStart, { passive: true }],
    ["touchmove", onTouchMove, { passive: true }],
    // Captured, from wherever the last finger lifts: a release must not depend
    // on the page letting the event bubble.
    ["touchend", onTouchEnd, { passive: true, capture: true }],
    ["touchcancel", onTouchEnd, { passive: true, capture: true }],
    ["pointerdown", onPointerDown, { passive: true }],
    ["pointerup", onPointerUp, { passive: true }],
    ["pointercancel", onPointerUp, { passive: true }],
    ["scrollend", onScrollEnd, { passive: true, capture: true }],
    ["visibilitychange", onVisibilityChange, { passive: true, capture: true }],
  ];
  let windowAttached = false;

  /**
   * Announce that the caller's own mutation just REMOVED content, so the clamp
   * it produced is not classified as a gesture. Announced rather than inferred
   * because scrollHeight and clientHeight are integer-rounded while scrollTop
   * is fractional, so under zoom a clamp can read as a user scrolling up.
   * Call it AFTER the mutation with the offset read BEFORE it, having collapsed
   * the overlays anchored in that space.
   */
  function noteContentShrink(scrollTopBefore: number): void {
    if (!scrollEl) {
      return;
    }
    if (scrollEl.scrollTop < scrollTopBefore) {
      shrinkArmed = true;
    }
    // A correctly reconciled offset can read a fraction past the end because
    // scrollHeight and clientHeight are integer-rounded; do not correct that.
    if (distanceFromBottom() < -CLAMP_EPSILON_PX) {
      rangeCorrectionOwed = true;
    }
  }

  /**
   * Move the offset back inside the range when an announced row removal left
   * it past the end: CSSOM View does not say when a UA must reconcile an
   * established offset after the overflow shrinks, and WebKit can leave it there
   * until a manual scroll (stackoverflow.com/q/79752870). Call it in the same
   * pass as noteContentShrink and BEFORE the position invariants. Not folded
   * into stickToBottom, which is follow-gated while a HOLDING reader is stranded.
   */
  function reconcileScrollRange(): void {
    if (!scrollEl || !rangeCorrectionOwed) {
      return;
    }
    rangeCorrectionOwed = false;
    settle();
    write(bottomOffset(), true);
  }

  /**
   * Pin the viewport to the bottom iff following, deferred while a press or its
   * momentum owns the offset: WebKit applies a programmatic scroll over an
   * in-flight pan with no user-scroll guard (ScrollingTreeScrollingNode::
   * handleScrollPositionRequest, https://github.com/WebKit/WebKit/blob/main/Source/WebCore/page/scrolling/ScrollingTreeScrollingNode.cpp).
   * Only a positive distance is corrected: an offset PAST the end is
   * reconcileScrollRange's, and here it would fight an overscroll bounce.
   */
  function stickToBottom(): void {
    if (!scrollEl) {
      return;
    }
    settle();
    if (!following || distanceFromBottom() <= 0) {
      return;
    }
    if (pressHeld || momentumPending) {
      pinOwed = true;
      return;
    }
    write(bottomOffset(), false);
  }

  function scrollToBottom(): void {
    if (!scrollEl) {
      return;
    }
    settle();
    write(bottomOffset(), false);
    setFollowing(true);
  }

  function isUserScrolledUp(): boolean {
    return !following;
  }

  function currentScrollTop(): number {
    return scrollEl ? scrollEl.scrollTop : 0;
  }

  /**
   * Scroll anchoring by hand: Safari before 27 has no `overflow-anchor`
   * (https://developer.mozilla.org/docs/Web/CSS/Reference/Properties/overflow-anchor#browser_compatibility),
   * so every row evicted above a scrolled-up reader slid their position one
   * line up. `following` is deliberately NOT changed; deriving it would let a
   * correction that lands at the bottom silently re-engage auto-follow.
   */
  function adjustForContentShift(deltaPx: number): void {
    if (!scrollEl || following || deltaPx === 0) {
      return;
    }
    settle();
    write(scrollEl.scrollTop + deltaPx, true);
  }

  /**
   * Restore both the offset and the follow state explicitly: writing only the
   * position cannot express a view holding AT the bottom, which a shrink under
   * a scrolled-up reader produces. A non-finite offset is ignored (the DOM
   * would coerce NaN to 0 and jump to the top); the follow state still applies.
   */
  function restoreView(view: { top: number; following: boolean }): void {
    if (!scrollEl) {
      return;
    }
    settle();
    if (Number.isFinite(view.top)) {
      write(view.top, true);
    }
    intentUnconfirmed = false;
    setFollowing(view.following);
  }

  function dispose(): void {
    if (scrollEl) {
      scrollEl.removeEventListener("scroll", onScroll);
    }
    if (windowAttached && win) {
      for (const [type, fn, options] of windowListeners) {
        win.removeEventListener(type, fn as EventListener, { capture: options.capture ?? false });
      }
    }
    windowAttached = false;
    releaseTouchTargets();
    clearQuietTimer();
    pressHeld = false;
    momentumPending = false;
    pinOwed = false;
    intentUnconfirmed = false;
    scrollEl = null;
    onFollowChange = null;
    onPosition = null;
    following = true;
  }

  try {
    // Exactly one listener on the container, registered first.
    opts.scrollEl.addEventListener("scroll", onScroll, { passive: true });
    if (win) {
      windowAttached = true;
      for (const [type, fn, options] of windowListeners) {
        win.addEventListener(type, fn as EventListener, options);
      }
    }
  } catch (err) {
    dispose();
    throw err;
  }

  return {
    noteContentShrink,
    reconcileScrollRange,
    stickToBottom,
    scrollToBottom,
    isUserScrolledUp,
    currentScrollTop,
    adjustForContentShift,
    restoreView,
    dispose,
  };
}
