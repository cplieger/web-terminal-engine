// Scroll state tracker.
//
// Tracks whether the user has manually scrolled up (away from the
// bottom) and exposes the scroll helpers the render layer uses.

const BOTTOM_TOLERANCE_PX = 100;
const USER_SCROLL_DEBOUNCE_MS = 150;

let scrollEl: HTMLElement | null = null;
let userScrolledUp = false;
let userScrollingUntil = 0;
let suppressUntil = 0;
let onUserScrollChange: ((scrolledUp: boolean) => void) | null = null;

function isAtBottom(): boolean {
  if (!scrollEl) {
    return true;
  }
  return scrollEl.scrollTop + scrollEl.clientHeight >= scrollEl.scrollHeight - BOTTOM_TOLERANCE_PX;
}

export function init(opts: {
  scrollEl: HTMLElement;
  onUserScrollChange?: (scrolledUp: boolean) => void;
}): void {
  scrollEl = opts.scrollEl;
  onUserScrollChange = opts.onUserScrollChange ?? null;

  scrollEl.addEventListener(
    "scroll",
    () => {
      if (Date.now() < suppressUntil) {
        return;
      }
      userScrollingUntil = Date.now() + USER_SCROLL_DEBOUNCE_MS;
      const wasScrolledUp = userScrolledUp;
      userScrolledUp = !isAtBottom();
      if (wasScrolledUp !== userScrolledUp && onUserScrollChange) {
        onUserScrollChange(userScrolledUp);
      }
    },
    { passive: true },
  );

  scrollEl.addEventListener(
    "touchstart",
    () => {
      userScrollingUntil = Date.now() + 60_000;
    },
    { passive: true },
  );
  scrollEl.addEventListener(
    "touchend",
    () => {
      userScrollingUntil = Date.now() + USER_SCROLL_DEBOUNCE_MS;
    },
    { passive: true },
  );
  scrollEl.addEventListener(
    "touchcancel",
    () => {
      userScrollingUntil = Date.now() + USER_SCROLL_DEBOUNCE_MS;
    },
    { passive: true },
  );
}

/** Force scroll-to-bottom and re-engage auto-follow. */
export function scrollToBottom(): void {
  if (!scrollEl) {
    return;
  }
  userScrolledUp = false;
  userScrollingUntil = 0;
  if (onUserScrollChange) {
    onUserScrollChange(false);
  }
  scrollEl.scrollTop = scrollEl.scrollHeight;
}

/** Suppress user-scroll detection for the next `ms` milliseconds. */
export function suppressScroll(ms: number): void {
  suppressUntil = Date.now() + ms;
}

export function isUserScrolledUp(): boolean {
  return userScrolledUp;
}

export function isInUserScroll(): boolean {
  return Date.now() < userScrollingUntil;
}
