// The scroll controller's ARMING discipline: the announced-shrink pass-through
// on the ordering a browser produces (the announcement comes BEFORE the clamp's
// scroll event, which CSSOM delivers at the next frame, and matters only when
// the residual exceeds CLAMP_EPSILON_PX); the one-event lifetime of both arms
// (an arm that fails to clear swallows the next real gesture); the position seam
// and its placement after the pass-through return; the dispose detach; and the
// exact edges, 24px bottom tolerance and 1px epsilon. The fixtures are local
// because the shared one dispatches the clamp's event before a caller can announce.

import { describe, it, expect } from "vitest";
import { createScrollController, type ScrollController } from "./scroll.js";
import { registerForDispose } from "./test-helpers/engine-fixture.js";

let scroll: ScrollController;

interface ManualScroller {
  el: HTMLElement;
  /** Move the offset and deliver the event, the way a user gesture does. */
  userScrollTo(next: number): void;
  /** Deliver a scroll event for the position the container already holds. */
  fireScroll(): void;
  /** Remove content and land the offset where the browser's clamp would,
   *  WITHOUT delivering the event yet. */
  shrinkTo(nextHeight: number, settledTop: number): void;
  /** Add content below the viewport. Appending fires no scroll event. */
  growTo(nextHeight: number): void;
  readonly top: number;
}

/** A scroll container whose height and offset are set independently, and which
 *  never dispatches an event by itself: every event in this file is explicit,
 *  so the order of announcement and delivery is the test's to choose. */
function makeManualScroller(scrollHeight: number, clientHeight: number): ManualScroller {
  const el = document.createElement("div");
  let height = scrollHeight;
  let top = 0;
  Object.defineProperty(el, "scrollHeight", { get: () => height, configurable: true });
  Object.defineProperty(el, "clientHeight", { get: () => clientHeight, configurable: true });
  Object.defineProperty(el, "scrollTop", {
    get: () => top,
    set: (v: number) => {
      top = v;
    },
    configurable: true,
  });
  return {
    el,
    userScrollTo(next: number): void {
      top = next;
      el.dispatchEvent(new Event("scroll"));
    },
    fireScroll(): void {
      el.dispatchEvent(new Event("scroll"));
    },
    shrinkTo(nextHeight: number, settledTop: number): void {
      height = nextHeight;
      top = settledTop;
    },
    growTo(nextHeight: number): void {
      height = nextHeight;
    },
    get top(): number {
      return el.scrollTop;
    },
  };
}

describe("announced content shrink (noteContentShrink)", () => {
  it("preserves follow when the clamp leaves a residual bigger than the epsilon", () => {
    // The case the announcement exists for. scrollHeight/clientHeight are
    // integer-rounded while scrollTop is fractional, so under browser zoom or a
    // fractional DPR a clamp can settle further than CLAMP_EPSILON_PX from the
    // bottom the JS properties report. By position that is indistinguishable
    // from a user scrolling up; the announcement is what tells the two apart.
    const f = makeManualScroller(10000, 300);
    scroll = registerForDispose(createScrollController({ scrollEl: f.el }));
    f.userScrollTo(9700); // the live tail, following
    expect(scroll.isUserScrolledUp()).toBe(false);

    const before = f.top;
    f.shrinkTo(1000, 695); // the clamp settles 5px short of the new bottom (700)
    scroll.noteContentShrink(before);
    f.fireScroll(); // the clamp's own event, delivered after the announcement
    expect(scroll.isUserScrolledUp()).toBe(false);
  });

  it("disengages for the same movement when nothing announced it", () => {
    // The other half of the pair: without the announcement the residual rule
    // decides, and a 5px gap below is a user pulling away from the tail.
    const f = makeManualScroller(10000, 300);
    scroll = registerForDispose(createScrollController({ scrollEl: f.el }));
    f.userScrollTo(9700);
    expect(scroll.isUserScrolledUp()).toBe(false);

    f.shrinkTo(1000, 695);
    f.fireScroll();
    expect(scroll.isUserScrolledUp()).toBe(true);
  });

  it("arms nothing when the caller's removal did not move the offset", () => {
    // Rows removed below the viewport, or a removal smaller than the bottom
    // gap: no clamp, so no event to consume an arm — and an arm left standing
    // swallows the user's next gesture.
    const f = makeManualScroller(1000, 300);
    scroll = registerForDispose(createScrollController({ scrollEl: f.el }));
    f.userScrollTo(700); // the tail, following
    scroll.noteContentShrink(f.top); // announced, but the offset did not move
    f.userScrollTo(200); // a real upward gesture
    expect(scroll.isUserScrolledUp()).toBe(true);
  });

  it("is consumed by the first event, so a later clamp-shaped move still decides", () => {
    const f = makeManualScroller(10000, 300);
    scroll = registerForDispose(createScrollController({ scrollEl: f.el }));
    f.userScrollTo(9700);
    const before = f.top;
    f.shrinkTo(5000, 4700);
    scroll.noteContentShrink(before);
    f.fireScroll(); // consumes the announcement
    f.userScrollTo(1000); // an unannounced upward move
    expect(scroll.isUserScrolledUp()).toBe(true);
  });
});

describe("construction leaves no arm standing", () => {
  it("treats the first event after construction as a real gesture", () => {
    // A container mounted at its tail: the very first scroll event is the
    // user's, not the echo of a library write, so nothing may swallow it.
    const f = makeManualScroller(1000, 300);
    f.el.scrollTop = 700; // mounted at the bottom, as a live session is
    scroll = registerForDispose(createScrollController({ scrollEl: f.el }));
    f.userScrollTo(200);
    expect(scroll.isUserScrolledUp()).toBe(true);
  });
});

describe("the library-write pass-through lasts exactly one event", () => {
  it("lets the gesture after the echo re-engage follow", () => {
    // adjustForContentShift writes the container on the library's behalf and
    // arms a one-event pass-through for the echo. An arm that survived the echo
    // would swallow the user's next scroll — here, their return to the tail.
    const f = makeManualScroller(1000, 300);
    scroll = registerForDispose(createScrollController({ scrollEl: f.el }));
    f.userScrollTo(700); // the tail, following
    f.userScrollTo(400); // scrolled up to read, holding
    expect(scroll.isUserScrolledUp()).toBe(true);

    scroll.adjustForContentShift(-34); // two evicted rows above the reading position
    expect(f.top).toBe(366);
    f.fireScroll(); // the write's own echo, consumed by the arm

    f.userScrollTo(700); // back down to the tail
    expect(scroll.isUserScrolledUp()).toBe(false);
  });
});

describe("the position seam (onScrollPosition)", () => {
  it("fires for every scroll event that moved the offset", () => {
    const f = makeManualScroller(1000, 300);
    let positions = 0;
    scroll = registerForDispose(
      createScrollController({
        scrollEl: f.el,
        onScrollPosition: () => {
          positions += 1;
        },
      }),
    );
    f.userScrollTo(700);
    f.userScrollTo(400);
    expect(positions).toBe(2);
  });

  it("does not fire for the echo of the library's own write", () => {
    // Placement is load-bearing: a paged-in prepend goes through
    // adjustForContentShift, and notifying for its echo would turn every
    // prepend into a fresh fetch trigger — a self-feeding loop.
    const f = makeManualScroller(1000, 300);
    let positions = 0;
    scroll = registerForDispose(
      createScrollController({
        scrollEl: f.el,
        onScrollPosition: () => {
          positions += 1;
        },
      }),
    );
    f.userScrollTo(700);
    f.userScrollTo(400); // holding
    positions = 0;
    scroll.adjustForContentShift(-34);
    f.fireScroll();
    expect(positions).toBe(0);
  });

  it("fires after a dispose only for the live controller's listener", () => {
    // A disposed controller (a re-mount, or a tabbed shell rebinding) must have
    // detached its scroll listener; two live listeners would notify twice per event.
    const f = makeManualScroller(1000, 300);
    let positions = 0;
    const previous = createScrollController({
      scrollEl: f.el,
      onScrollPosition: () => {
        positions += 1;
      },
    });
    previous.dispose();
    scroll = registerForDispose(
      createScrollController({
        scrollEl: f.el,
        onScrollPosition: () => {
          positions += 1;
        },
      }),
    );
    f.userScrollTo(700);
    expect(positions).toBe(1);
  });
});

describe("an unmoved scroll event infers nothing", () => {
  it("keeps following when output has grown below the viewport", () => {
    // Following with a real gap below is the normal state between a flush and
    // its pin. A scroll event that did not move the offset carries no intent,
    // and reading it as an upward move would disengage auto-follow mid-stream.
    const f = makeManualScroller(1000, 300);
    scroll = registerForDispose(createScrollController({ scrollEl: f.el }));
    f.userScrollTo(700); // the tail, following
    f.growTo(2000); // new output: appending fires no scroll event
    f.fireScroll(); // something else fires one (a restyle, a resize)
    expect(scroll.isUserScrolledUp()).toBe(false);
  });
});

describe("the two constants' edges", () => {
  it("counts exactly BOTTOM_TOLERANCE_PX from the bottom as the bottom", () => {
    // A downward move that stops 24px short still re-engages follow; 25px does
    // not (scroll.test.ts covers 31px).
    const f = makeManualScroller(1000, 300);
    scroll = registerForDispose(createScrollController({ scrollEl: f.el }));
    f.userScrollTo(700); // the tail
    f.userScrollTo(669); // 31px of gap: holding
    expect(scroll.isUserScrolledUp()).toBe(true);
    f.userScrollTo(676); // downward, exactly 24px of gap
    expect(scroll.isUserScrolledUp()).toBe(false);
  });

  it("keeps follow for an upward move that lands exactly CLAMP_EPSILON_PX short", () => {
    // The subpixel residual the epsilon absorbs: 1px of gap is not a reader
    // pulling away from the tail.
    const f = makeManualScroller(1000, 300);
    scroll = registerForDispose(createScrollController({ scrollEl: f.el }));
    f.userScrollTo(700); // the tail, no gap
    f.userScrollTo(699); // upward, exactly 1px of gap
    expect(scroll.isUserScrolledUp()).toBe(false);
    f.userScrollTo(698); // upward, 2px of gap: a real move
    expect(scroll.isUserScrolledUp()).toBe(true);
  });
});
