// Scroll controller: the single owner of one scroll container's scrollTop and
// of one piece of state, `following`, derived from scroll events by position
// AND direction with no debounce, suppress timer or programmatic-vs-user flag.
// A library write that must not change the state arms a one-event pass-through
// for the scroll event its own write produces, only when the write moved the
// position, so the arm can never linger and swallow a later real gesture; every
// write from outside this module is classified by direction like a user scroll.
const BOTTOM_TOLERANCE_PX = 24;
// An upward move only disengages follow when a real gap is left below it.
// Above 0 to absorb fractional-layout rounding on a shrink clamp; far below
// any real one-frame user scroll increment.
const CLAMP_EPSILON_PX = 1;

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
  /** Removes the scroll listener and drops the element and callbacks; every method is a no-op after. */
  dispose(): void;
}

/**
 * Creates the scroll controller for `opts.scrollEl`, following from
 * construction. Throws, holding nothing, when the listener cannot be attached.
 */
export function createScrollController(opts: ScrollControllerOptions): ScrollController {
  let scrollEl: HTMLElement | null = opts.scrollEl;
  let following = true;
  let lastScrollTop = opts.scrollEl.scrollTop;
  // Armed by a library write that must PRESERVE the follow state across the
  // single scroll event it produces; never armed when the write did not move
  // the position, because then no event fires to consume it.
  let preserveFollowOnce = false;
  // Armed by noteContentShrink when the caller's own row removal moved the
  // position: the next event is that clamp, not a gesture.
  let shrinkArmed = false;
  // Armed by noteContentShrink when the removal left the offset PAST the end
  // of the content; consumed by reconcileScrollRange in the same pass.
  // Independent of shrinkArmed: a partial reconciliation sets both.
  let rangeCorrectionOwed = false;
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

  const onScroll = (): void => {
    if (!scrollEl) {
      return;
    }
    const top = scrollEl.scrollTop;
    const prev = lastScrollTop;
    lastScrollTop = top;
    // Read-and-clear: the announcement does not survive this event, whatever
    // it turns out to be, so a coalesced event netting downward leaves no arm.
    const wasShrink = shrinkArmed;
    shrinkArmed = false;
    if (preserveFollowOnce) {
      preserveFollowOnce = false;
      return;
    }
    // Fired AFTER the pass-through above (a paged-in prepend goes through
    // adjustForContentShift, and notifying for it would make every prepend a
    // fetch trigger) and separately from onFollowChange (a reader browsing
    // history never toggles follow, and idle browsing is when paging must
    // work). It also fires for the browser's own clamps, so it is a position
    // change, not a gesture (docs/paged-scrollback.md §5.4).
    if (onPosition) {
      onPosition();
    }
    if (top < prev) {
      // Upward: a user pulling away or a shrink clamp. ANY upward move leaving
      // a real gap disengages at once: the renderer pins every frame, and a
      // tolerance-only rule let each pin reset the baseline under a user
      // scrolling up. Neither may ENGAGE follow: a shrink clamp lands at the
      // bottom by an upward move, indistinguishable from a user arriving by
      // position but not by direction. An announced shrink skips the epsilon,
      // which still backs an un-announced one (a consumer restyling the surface).
      if (wasShrink) {
        return;
      }
      if (distanceFromBottom() > CLAMP_EPSILON_PX) {
        setFollowing(false);
      }
      return;
    }
    if (top > prev) {
      // Downward: only a move that lands at the bottom re-engages.
      setFollowing(atBottom());
    }
    // Unmoved: no intent, and the position may be one a shrink clamp set.
  };
  let scrollHandler: (() => void) | null = onScroll;

  // Writes the offset on the library's own behalf. `lastScrollTop` is synced to
  // the POST-clamp value so the next event's direction is computed from where
  // the container landed; the pass-through is armed only when it really moved.
  function writePreservingFollow(next: number): void {
    if (!scrollEl) {
      return;
    }
    const before = scrollEl.scrollTop;
    scrollEl.scrollTop = next;
    const after = scrollEl.scrollTop;
    lastScrollTop = after;
    if (after !== before) {
      preserveFollowOnce = true;
    }
  }

  /**
   * Announce that the caller's own mutation just REMOVED content, so the clamp
   * it produced is not classified as a gesture. Announced rather than inferred
   * because scrollHeight and clientHeight are integer-rounded while scrollTop
   * is fractional, so under zoom a clamp can read as a user scrolling up
   * (docs/scroll-position-fidelity.md §1.3). Call it AFTER the mutation with the
   * offset read BEFORE it, having collapsed the overlays anchored in that space.
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
    writePreservingFollow(bottomOffset());
  }

  /**
   * Pin the viewport to the bottom iff following. Only the positive branch of
   * distanceFromBottom is this function's business: an offset PAST the end is
   * a geometry error owned by reconcileScrollRange, and correcting it here
   * would fight an overscroll bounce, which presents the same arithmetic.
   */
  function stickToBottom(): void {
    if (!scrollEl || !following) {
      return;
    }
    if (distanceFromBottom() > 0) {
      scrollEl.scrollTop = bottomOffset();
    }
  }

  function scrollToBottom(): void {
    if (!scrollEl) {
      return;
    }
    scrollEl.scrollTop = bottomOffset();
    setFollowing(true);
  }

  function isUserScrolledUp(): boolean {
    return !following;
  }

  function currentScrollTop(): number {
    return scrollEl ? scrollEl.scrollTop : 0;
  }

  /**
   * Scroll anchoring by hand: WebKit has never shipped `overflow-anchor`, so on
   * Safari every row evicted above a scrolled-up reader slid their position one
   * line up. `following` is deliberately NOT changed; deriving it would let a
   * correction that lands at the bottom silently re-engage auto-follow.
   */
  function adjustForContentShift(deltaPx: number): void {
    if (!scrollEl || following || deltaPx === 0) {
      return;
    }
    writePreservingFollow(scrollEl.scrollTop + deltaPx);
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
    if (Number.isFinite(view.top)) {
      writePreservingFollow(view.top);
    }
    setFollowing(view.following);
  }

  function dispose(): void {
    if (scrollEl && scrollHandler) {
      scrollEl.removeEventListener("scroll", scrollHandler);
    }
    scrollEl = null;
    scrollHandler = null;
    onFollowChange = null;
    onPosition = null;
    following = true;
  }

  try {
    opts.scrollEl.addEventListener("scroll", onScroll, { passive: true });
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
