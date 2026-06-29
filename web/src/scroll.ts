// Scroll controller: the single owner of the scroll container's scrollTop.
//
// One piece of state: `following`. The user is "following" when the viewport is
// at (or within a small tolerance of) the bottom; otherwise they have scrolled
// up to read and are "holding". The state is derived purely from scroll
// position on every scroll event — there is no debounce window, no suppress
// timer, and no programmatic-vs-user flag. That heuristic soup (a 100px
// tolerance, a 150ms debounce, a 60-second touch window) was the source of the
// view-jumping and scroll-interruption bugs (the legacy heuristic-soup
// failure mode; see the #web-terminal-engine steering doc, "Design rationale").
//
// The renderer calls stickToBottom() once after each flush: if following, pin
// to the new bottom; if holding, do nothing and let native scroll anchoring
// (overflow-anchor) hold the reading position when history is inserted above.
// Appending content at the bottom does not fire a scroll event, so following
// stays true across new output and the post-flush pin lands correctly. Pinning
// to the bottom produces a scroll event whose recomputation yields
// following=true again (no churn). Scrolling up past the tolerance flips to
// holding; scrolling back flips to following.

const BOTTOM_TOLERANCE_PX = 24;

let scrollEl: HTMLElement | null = null;
let following = true;
let onFollowChange: ((scrolledUp: boolean) => void) | null = null;
let scrollHandler: (() => void) | null = null;

function distanceFromBottom(): number {
  if (!scrollEl) {
    return 0;
  }
  return scrollEl.scrollHeight - scrollEl.scrollTop - scrollEl.clientHeight;
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

/**
 * Initialize the scroll controller on the scroll container. The optional
 * callback fires whenever the follow state toggles (its argument is true when
 * the user has scrolled up / disengaged auto-follow).
 *
 * @param opts.scrollEl            Element whose scroll position is observed and owned.
 * @param opts.onUserScrollChange  Optional callback fired on follow/hold toggle.
 */
export function init(opts: {
  scrollEl: HTMLElement;
  onUserScrollChange?: (scrolledUp: boolean) => void;
}): void {
  // Detach any prior listener (re-init in tests / re-mount).
  if (scrollEl && scrollHandler) {
    scrollEl.removeEventListener("scroll", scrollHandler);
  }
  scrollEl = opts.scrollEl;
  onFollowChange = opts.onUserScrollChange ?? null;
  following = true;
  scrollHandler = () => {
    setFollowing(atBottom());
  };
  scrollEl.addEventListener("scroll", scrollHandler, { passive: true });
}

/**
 * Pin the viewport to the bottom iff the user is following. Called by the
 * renderer after each flush. A no-op when holding (scrolled up) or already at
 * the bottom, so it never fights the user and never scrolls redundantly.
 */
export function stickToBottom(): void {
  if (!scrollEl || !following) {
    return;
  }
  if (distanceFromBottom() > 0) {
    scrollEl.scrollTop = scrollEl.scrollHeight;
  }
}

/**
 * Force scroll to the bottom and re-engage following. Used by the explicit
 * jump-to-bottom control.
 */
export function scrollToBottom(): void {
  if (!scrollEl) {
    return;
  }
  scrollEl.scrollTop = scrollEl.scrollHeight;
  setFollowing(true);
}

/** Whether the user has scrolled away from the bottom (auto-follow disengaged). */
export function isUserScrolledUp(): boolean {
  return !following;
}
