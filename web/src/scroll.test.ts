// scrollHeight and clientHeight are declared, not measured: the geometry IS the
// premise. The mock starts at scrollTop 0 with 700px of range, which a real
// container never is, so follow-transition tests reach the bottom first.

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { createScrollController, type ScrollController } from "./scroll.js";
import { registerForDispose } from "./test-helpers/engine-fixture.js";
import { makeClampingScrollEl, makeDeferredClampScrollEl } from "./test-helpers/scroll-fixture.js";

let scroll: ScrollController;

function makeScrollEl(scrollHeight: number, clientHeight: number): HTMLElement {
  const el = document.createElement("div");
  let top = 0;
  Object.defineProperty(el, "scrollHeight", { get: () => scrollHeight, configurable: true });
  Object.defineProperty(el, "clientHeight", { get: () => clientHeight, configurable: true });
  Object.defineProperty(el, "scrollTop", {
    get: () => top,
    set: (v: number) => {
      top = v;
    },
    configurable: true,
  });
  return el;
}

function scrollTo(el: HTMLElement, top: number): void {
  el.scrollTop = top;
  el.dispatchEvent(new Event("scroll"));
}

describe("scroll controller (brick 4)", () => {
  let el: HTMLElement;
  let changes: boolean[];

  beforeEach(() => {
    el = makeScrollEl(1000, 300); // 700px of scroll range
    changes = [];
    scroll = registerForDispose(
      createScrollController({ scrollEl: el, onUserScrollChange: (up) => changes.push(up) }),
    );
  });

  it("starts in the following state", () => {
    expect(scroll.isUserScrolledUp()).toBe(false);
  });

  it("flips to holding when the user scrolls up past the tolerance, and back", () => {
    scrollTo(el, 700); // at the bottom, following
    scrollTo(el, 0); // distance from bottom = 700 -> holding
    expect(scroll.isUserScrolledUp()).toBe(true);
    expect(changes).toEqual([true]);
    scrollTo(el, 700); // a DOWNWARD move landing at the bottom -> following
    expect(scroll.isUserScrolledUp()).toBe(false);
    expect(changes).toEqual([true, false]);
  });

  it("treats within-tolerance as following (24px)", () => {
    scrollTo(el, 700); // at the bottom, following
    scrollTo(el, 680); // 20px up, inside the tolerance but a real gap -> holding
    expect(scroll.isUserScrolledUp()).toBe(true);
    scrollTo(el, 690); // downward but 10px short of the bottom, inside the
    expect(scroll.isUserScrolledUp()).toBe(false); // tolerance -> following
    scrollTo(el, 669); // 21px up, distance 31 (> 24) -> holding
    expect(scroll.isUserScrolledUp()).toBe(true);
  });

  it("disengages follow on ANY upward scroll that leaves content below", () => {
    // Even a few px of upward movement inside the bottom tolerance is user
    // intent to hold. With a tolerance-only rule this stayed "following" and
    // the next render pin yanked the user back down (see the streaming test
    // below for the full race).
    scrollTo(el, 700); // at the bottom, following
    scrollTo(el, 694); // 6px up — still within the 24px tolerance
    expect(scroll.isUserScrolledUp()).toBe(true);
  });

  it("a slow upward drag escapes the per-frame pin during heavy streaming", () => {
    // The streaming fight: each frame the renderer flushes and pins to the
    // bottom, so a slow drag's per-frame increment restarted from the bottom
    // and (tolerance-only) never accumulated past 24px — the user was yanked
    // down every few ms. Direction-based disengage flips holding on the FIRST
    // upward tick, so the next pin is a no-op.
    scrollTo(el, 700); // bottom
    scroll.stickToBottom(); // frame N pin (no-op at the bottom)
    scrollTo(el, 692); // first drag tick, 8px up
    scroll.stickToBottom(); // frame N+1 pin must not fight the drag
    expect(el.scrollTop).toBe(692);
    expect(scroll.isUserScrolledUp()).toBe(true);
  });

  it("keeps following when a content shrink clamps scrollTop to the new bottom", () => {
    // Top-row eviction / a clear shrinks scrollHeight; the browser clamps
    // scrollTop DOWN to the new maximum — an upward move that is NOT the user
    // and lands exactly at the bottom. Auto-follow must survive it.
    let sh = 1000;
    const el2 = document.createElement("div");
    let top = 0;
    Object.defineProperty(el2, "scrollHeight", { get: () => sh, configurable: true });
    Object.defineProperty(el2, "clientHeight", { get: () => 300, configurable: true });
    Object.defineProperty(el2, "scrollTop", {
      get: () => top,
      set: (v: number) => {
        top = v;
      },
      configurable: true,
    });
    scroll = registerForDispose(createScrollController({ scrollEl: el2 }));
    scrollTo(el2, 700); // at the bottom, following
    sh = 900; // eviction shrinks the content
    scrollTo(el2, 600); // the browser's clamp to the new bottom (upward move)
    expect(scroll.isUserScrolledUp()).toBe(false); // still following
    scroll.stickToBottom();
    expect(el2.scrollTop).toBe(600); // already at the (new) bottom, no churn
  });

  it("stickToBottom pins to the bottom while following", () => {
    expect(el.scrollTop).toBe(0);
    scroll.stickToBottom();
    // The bottom is scrollHeight - clientHeight, written out. This fixture stores
    // whatever it is handed, so it is the one that can tell the difference
    // between the pin computing the bottom and the pin writing scrollHeight and
    // trusting the container to clamp it back.
    expect(el.scrollTop).toBe(700);
  });

  it("stickToBottom does nothing while holding (does not yank the reader)", () => {
    scrollTo(el, 700);
    scrollTo(el, 100); // holding
    scroll.stickToBottom();
    expect(el.scrollTop).toBe(100); // unchanged
  });

  it("scrollToBottom forces the bottom and re-engages following", () => {
    scrollTo(el, 700); // at the bottom, following
    scrollTo(el, 0); // holding
    expect(scroll.isUserScrolledUp()).toBe(true);
    scroll.scrollToBottom();
    expect(el.scrollTop).toBe(700);
    expect(scroll.isUserScrolledUp()).toBe(false);
  });

  it("a content shrink that clamps a HOLDING reader to the bottom keeps holding", () => {
    // The asymmetry's whole reason to exist. An ED3 or a cap eviction collapses
    // scrollHeight and the browser clamps scrollTop DOWN to the new maximum. By
    // position that clamp is indistinguishable from the user arriving at the
    // tail; by DIRECTION it is not, since a returning user moves down and a
    // clamp moves up. Deriving from position alone re-engages auto-follow under
    // a reader who scrolled up (measured: 20000px up, an ED3, pinned to the tail).
    let sh = 1000;
    const el2 = document.createElement("div");
    let top = 0;
    Object.defineProperty(el2, "scrollHeight", { get: () => sh, configurable: true });
    Object.defineProperty(el2, "clientHeight", { get: () => 300, configurable: true });
    Object.defineProperty(el2, "scrollTop", {
      get: () => top,
      set: (v: number) => {
        top = v;
      },
      configurable: true,
    });
    scroll = registerForDispose(createScrollController({ scrollEl: el2 }));
    scrollTo(el2, 700); // at the bottom, following
    scrollTo(el2, 200); // the user scrolls up to read
    expect(scroll.isUserScrolledUp()).toBe(true);

    sh = 300; // the app erases its scrollback: only the live screen is left
    scrollTo(el2, 0); // the browser clamps to the new maximum (an upward move)
    expect(scroll.isUserScrolledUp()).toBe(true); // still holding

    // And the pin must keep its hands off, however much output follows.
    sh = 2000;
    scroll.stickToBottom();
    expect(el2.scrollTop).toBe(0);
  });

  it("a scroll event that did not move the position infers nothing", () => {
    // The state that makes this matter: HOLDING while the position happens to be
    // the bottom, which a content shrink produces (see the test above). An
    // unmoved event there reports "at the bottom", and deriving follow from
    // position alone would re-engage on it. Asserted after a shrink rather than
    // at a mid position, because at a mid position the old position-derive also
    // answers "holding" and the test would pass either way.
    let sh = 1000;
    const el2 = document.createElement("div");
    let top = 0;
    Object.defineProperty(el2, "scrollHeight", { get: () => sh, configurable: true });
    Object.defineProperty(el2, "clientHeight", { get: () => 300, configurable: true });
    Object.defineProperty(el2, "scrollTop", {
      get: () => top,
      set: (v: number) => {
        top = Math.max(0, Math.min(v, sh - 300)); // clamp like a real container
      },
      configurable: true,
    });
    scroll = registerForDispose(createScrollController({ scrollEl: el2 }));
    scrollTo(el2, 700); // at the bottom, following
    scrollTo(el2, 200); // the user scrolls up to read
    expect(scroll.isUserScrolledUp()).toBe(true);

    sh = 500; // content shrinks so 200 IS now the bottom, with no clamp needed
    el2.dispatchEvent(new Event("scroll")); // unmoved, and reporting the bottom
    expect(scroll.isUserScrolledUp()).toBe(true);
  });
});

// noteContentShrink is how the layer that REMOVES rows says the clamp about to
// arrive is not a user scrolling up. The epsilon's arithmetic signature is
// lossy: scrollHeight and clientHeight are integer-rounded while scrollTop is
// fractional, so under zoom or a fractional DPR a clamp presents as an upward
// move with a real gap and the reader is left "holding" with follow silently
// off. These use the CLAMPING fixture, because a clamp is the whole subject.
describe("noteContentShrink (announced clamps)", () => {
  it("an UNannounced upward move of the same magnitude still disengages", () => {
    // Deleting the direction rule must fail this one. The announced-shrink
    // branch is NOT provable with this fixture: makeClampingScrollEl.setScrollHeight
    // dispatches the clamp's scroll event from inside itself, before a caller can
    // announce, so scroll-arming.test.ts covers it with a fixture that does not.
    const f = makeClampingScrollEl(10000, 300);
    scroll = registerForDispose(createScrollController({ scrollEl: f.el }));
    f.userScrollTo(9700);
    expect(scroll.isUserScrolledUp()).toBe(false);

    f.userScrollTo(100); // the user drags up: nothing announced
    expect(scroll.isUserScrolledUp()).toBe(true);
  });

  it("does not arm when the removal cannot move the position", () => {
    // Rows removed BELOW the viewport, a removal smaller than the bottom gap, or
    // a wipe whose height is held up by an absolutely-positioned overlay: no
    // clamp, so no event, so an arm would linger and swallow the next gesture.
    const f = makeClampingScrollEl(10000, 300);
    scroll = registerForDispose(createScrollController({ scrollEl: f.el }));
    f.userScrollTo(9700);
    f.userScrollTo(4000); // holding, far from both ends
    expect(scroll.isUserScrolledUp()).toBe(true);

    const before = f.el.scrollTop;
    f.setScrollHeight(9000); // still far above scrollTop + clientHeight: no clamp
    scroll.noteContentShrink(before);
    expect(f.el.scrollTop).toBe(before); // nothing moved

    // Back to the bottom, then a genuine drag up. If the arm had lingered it
    // would swallow this and the reader would be stuck following.
    f.userScrollTo(8700);
    expect(scroll.isUserScrolledUp()).toBe(false);
    f.userScrollTo(8600);
    expect(scroll.isUserScrolledUp()).toBe(true);
  });

  it("never survives more than one event", () => {
    const f = makeClampingScrollEl(10000, 300);
    scroll = registerForDispose(createScrollController({ scrollEl: f.el }));
    f.userScrollTo(9700);
    const before = f.el.scrollTop;
    f.setScrollHeight(5000);
    scroll.noteContentShrink(before); // armed, and consumed by the clamp's event
    f.el.dispatchEvent(new Event("scroll"));
    // A real upward gesture immediately after must register.
    f.userScrollTo(1000);
    expect(scroll.isUserScrolledUp()).toBe(true);
  });

  it("a subpixel clamp residual keeps follow even unannounced", () => {
    // The fractional-layout case the epsilon is actually for: scrollHeight and
    // clientHeight are integers, scrollTop is not, so a clamp can land a fraction
    // of a pixel short of the bottom.
    const el = document.createElement("div");
    let top = 0;
    Object.defineProperty(el, "scrollHeight", { get: () => 1000, configurable: true });
    Object.defineProperty(el, "clientHeight", { get: () => 300, configurable: true });
    Object.defineProperty(el, "scrollTop", {
      get: () => top,
      set: (v: number) => {
        top = v;
      },
      configurable: true,
    });
    scroll = registerForDispose(createScrollController({ scrollEl: el }));
    el.scrollTop = 700;
    el.dispatchEvent(new Event("scroll"));
    expect(scroll.isUserScrolledUp()).toBe(false);
    el.scrollTop = 699.4; // 0.6px of residual: an upward move, still the bottom
    el.dispatchEvent(new Event("scroll"));
    expect(scroll.isUserScrolledUp()).toBe(false);
  });
});

describe("per-view scroll memory seam (currentScrollTop / a consumer's own write)", () => {
  let el: HTMLElement;

  beforeEach(() => {
    el = makeScrollEl(1000, 300);
    scroll = registerForDispose(createScrollController({ scrollEl: el }));
  });

  it("reads the live offset through currentScrollTop", () => {
    scrollTo(el, 250);
    expect(scroll.currentScrollTop()).toBe(250);
  });

  it("a consumer's write to a mid position holds; one to the bottom re-engages follow", () => {
    // A write from outside the controller (a tabbed shell re-entering a tab
    // whose user had scrolled up) fires a scroll event, as any scrollTop
    // assignment does in a browser, and the follow/hold state re-derives from
    // it by direction like a user scroll.
    scrollTo(el, 700);
    el.scrollTop = 100;
    el.dispatchEvent(new Event("scroll")); // the fixture's setter fires none
    expect(scroll.currentScrollTop()).toBe(100);
    expect(scroll.isUserScrolledUp()).toBe(true);

    el.scrollTop = 700; // back to the bottom (distance 0)
    el.dispatchEvent(new Event("scroll"));
    expect(scroll.isUserScrolledUp()).toBe(false);
  });
});

describe("adjustForContentShift", () => {
  let el: HTMLElement;
  let changes: boolean[];

  beforeEach(() => {
    el = makeScrollEl(1000, 300);
    changes = [];
    scroll = registerForDispose(
      createScrollController({ scrollEl: el, onUserScrollChange: (up) => changes.push(up) }),
    );
  });

  it("moves the viewport by the height that vanished above the reading position", () => {
    scrollTo(el, 700);
    scrollTo(el, 400); // scroll up to read
    expect(scroll.isUserScrolledUp()).toBe(true);

    // Two 17px rows evicted from the top of history: the content the user is
    // reading is now 34px higher, so the viewport must follow it up by 34px.
    scroll.adjustForContentShift(-34);
    expect(el.scrollTop).toBe(366);
  });

  it("moves the other way when content is inserted above (the trim marker)", () => {
    scrollTo(el, 700);
    scrollTo(el, 400);
    scroll.adjustForContentShift(20);
    expect(el.scrollTop).toBe(420);
  });

  it("does not disengage or re-engage following", () => {
    scrollTo(el, 700);
    scrollTo(el, 400);
    changes.length = 0;
    scroll.adjustForContentShift(-34);
    el.dispatchEvent(new Event("scroll")); // the adjust's own event
    expect(scroll.isUserScrolledUp()).toBe(true);
    expect(changes).toEqual([]); // no follow flip, no churn
  });

  it("is a no-op while following, where the bottom pin owns the position", () => {
    scrollTo(el, 700); // at the bottom
    expect(scroll.isUserScrolledUp()).toBe(false);
    scroll.adjustForContentShift(-34);
    expect(el.scrollTop).toBe(700);
  });

  it("is a no-op for a zero delta", () => {
    scrollTo(el, 400);
    scroll.adjustForContentShift(0);
    expect(el.scrollTop).toBe(400);
  });

  it("does not re-engage follow when the correction lands the reader at the bottom", () => {
    // A reader holding a few px short of the tail while content grows ABOVE
    // them: the anchor correction pushes the viewport down by that growth and
    // can land exactly at the bottom. That is the library moving the viewport to
    // keep their line, not the user arriving at the tail, so follow must stay
    // off. Deriving from the resulting event's position would flip it on and the
    // next pin would take the position over.
    scrollTo(el, 700); // at the bottom, following
    scrollTo(el, 650); // holding, 50px up
    expect(scroll.isUserScrolledUp()).toBe(true);
    scroll.adjustForContentShift(50); // content grew above: follow the line down
    el.dispatchEvent(new Event("scroll")); // the adjust's own event
    expect(el.scrollTop).toBe(700); // at the bottom now
    expect(scroll.isUserScrolledUp()).toBe(true); // and still holding
  });
});

// restoreView is the per-view seam that carries BOTH halves of a saved view.
// It exists because "holding at the bottom" became reachable once a content
// shrink stopped re-engaging follow, and position alone cannot express it.
describe("restoreView", () => {
  let el: HTMLElement;
  let changes: boolean[];

  beforeEach(() => {
    el = makeScrollEl(1000, 300);
    changes = [];
    scroll = registerForDispose(
      createScrollController({ scrollEl: el, onUserScrollChange: (up) => changes.push(up) }),
    );
  });

  it("restores a mid position with follow off", () => {
    scroll.restoreView({ top: 100, following: false });
    el.dispatchEvent(new Event("scroll"));
    expect(el.scrollTop).toBe(100);
    expect(scroll.isUserScrolledUp()).toBe(true);
  });

  it("restores the bottom with follow on", () => {
    scrollTo(el, 200); // holding
    scroll.restoreView({ top: 700, following: true });
    el.dispatchEvent(new Event("scroll"));
    expect(el.scrollTop).toBe(700);
    expect(scroll.isUserScrolledUp()).toBe(false);
  });

  it("restores holding AT the bottom, which position alone cannot express", () => {
    scrollTo(el, 700); // at the bottom, following
    scroll.restoreView({ top: 700, following: false });
    el.dispatchEvent(new Event("scroll"));
    expect(el.scrollTop).toBe(700);
    expect(scroll.isUserScrolledUp()).toBe(true);
    // The pin must respect it, so the tail does not take the view back.
    scroll.stickToBottom();
    expect(el.scrollTop).toBe(700);
  });

  it("restores following at a mid position and lets the pin reconcile it", () => {
    scrollTo(el, 200); // holding
    scroll.restoreView({ top: 400, following: true });
    el.dispatchEvent(new Event("scroll"));
    expect(scroll.isUserScrolledUp()).toBe(false);
    scroll.stickToBottom();
    expect(el.scrollTop).toBe(700); // the bottom: scrollHeight - clientHeight
  });

  it("lets the gesture after a restore that did not move the offset register", () => {
    scrollTo(el, 700); // at the bottom, following
    scroll.restoreView({ top: 700, following: true });
    expect(scroll.isUserScrolledUp()).toBe(false);
    scrollTo(el, 300); // a real upward gesture
    expect(scroll.isUserScrolledUp()).toBe(true);
  });

  it("ignores a non-finite offset but still applies the follow state", () => {
    scrollTo(el, 400); // holding
    scroll.restoreView({ top: Number.NaN, following: true });
    expect(el.scrollTop).toBe(400); // not coerced to 0 / jumped to the top
    expect(scroll.isUserScrolledUp()).toBe(false);
  });
});

// reconcileScrollRange is the third arithmetic state of distanceFromBottom:
// positive means content below (pin), zero the tail, NEGATIVE an offset past the
// end of the container's own content, which a container that does not reconcile
// a shrink leaves the viewport parked at. Every test uses the DEFERRED-CLAMP
// fixture, because on a container that reconciles synchronously the negative
// state does not exist and a clamping fixture cannot fail for this.
describe("reconcileScrollRange (a container that does not reconcile a shrink)", () => {
  it("moves a FOLLOWING reader back onto the content after a big shrink", () => {
    const f = makeDeferredClampScrollEl(85000, 600);
    scroll = registerForDispose(createScrollController({ scrollEl: f.el }));
    f.userScrollTo(84400); // at the tail of a long session, following
    expect(scroll.isUserScrolledUp()).toBe(false);

    // The application erases its scrollback: only the live screen survives.
    const before = f.el.scrollTop;
    f.setScrollHeight(600);
    // The offset is untouched and illegal: 84400 into 600px of content.
    expect(f.el.scrollTop).toBe(84400);
    expect(f.maxTop()).toBe(0);

    scroll.noteContentShrink(before);
    scroll.reconcileScrollRange();
    expect(f.el.scrollTop).toBe(0); // the whole bug: this used to stay at 84400
  });

  it("keeps a following reader following across the correction", () => {
    const f = makeDeferredClampScrollEl(85000, 600);
    scroll = registerForDispose(createScrollController({ scrollEl: f.el }));
    f.userScrollTo(84400);

    const before = f.el.scrollTop;
    f.setScrollHeight(6000);
    scroll.noteContentShrink(before);
    scroll.reconcileScrollRange();
    expect(f.el.scrollTop).toBe(5400);
    // The correction produced a large UPWARD move, which the direction rule
    // would read as the user pulling away from the tail. It is the library's
    // own write, so its echo is measured from where the write landed.
    f.el.dispatchEvent(new Event("scroll"));
    expect(scroll.isUserScrolledUp()).toBe(false);
  });

  it("corrects a HOLDING reader too, and leaves them holding", () => {
    // The pin cannot own this correction: it is follow-gated, and a reader who
    // scrolled up to read is stranded over empty space by the same shrink. The
    // read anchor cannot own it either, because it deliberately stands down when
    // the lines it was holding were discarded rather than trimmed. Landing at
    // the tail with follow still OFF is the ratified degradation for a reading
    // position whose lines no longer exist.
    const f = makeDeferredClampScrollEl(85000, 600);
    scroll = registerForDispose(createScrollController({ scrollEl: f.el }));
    f.userScrollTo(84400); // following
    f.userScrollTo(20000); // scrolled up to read
    expect(scroll.isUserScrolledUp()).toBe(true);

    const before = f.el.scrollTop;
    f.setScrollHeight(600);
    scroll.noteContentShrink(before);
    scroll.reconcileScrollRange();
    expect(f.el.scrollTop).toBe(0);
    f.el.dispatchEvent(new Event("scroll"));
    expect(scroll.isUserScrolledUp()).toBe(true); // still holding
  });

  it("leaves an out-of-range offset alone when no caller announced a removal", () => {
    // The overscroll bounce. Safari reports an offset past the maximum while a
    // rubber-band is in flight, with no content change at all, and correcting
    // that would cut the user's own gesture. Nothing announced a shrink, so
    // nothing is armed, so the call is a no-op.
    const f = makeDeferredClampScrollEl(6000, 600);
    scroll = registerForDispose(createScrollController({ scrollEl: f.el }));
    f.userScrollTo(5400); // at the bottom
    // The bounce, forced past the maximum the way the platform does it (a write
    // would be clamped, which is the point of not using one here).
    Object.defineProperty(f.el, "scrollTop", { value: 5600, configurable: true });

    scroll.reconcileScrollRange();
    expect(f.el.scrollTop).toBe(5600); // untouched
  });

  it("corrects a bounce that coincides with a removal, and that is the choice", () => {
    // The accepted overlap, pinned so it reads as a decision. A bounce during a
    // row-removing pass (cap eviction under heavy streaming) satisfies the gate,
    // and the correction snaps the offset to the maximum the bounce was settling
    // towards anyway. The alternative (refuse whenever the offset was ALREADY
    // out of range) skips the repair for the rest of the session whenever the
    // two coincide, and a cut animation is the cheaper loss.
    const f = makeDeferredClampScrollEl(6000, 600);
    scroll = registerForDispose(createScrollController({ scrollEl: f.el }));
    f.userScrollTo(5400);
    Object.defineProperty(f.el, "scrollTop", {
      get: () => bounced,
      set: (v: number) => {
        bounced = Math.max(0, Math.min(v, f.maxTop()));
      },
      configurable: true,
    });
    let bounced = 5600; // mid-bounce, past the maximum

    const before = f.el.scrollTop;
    f.setScrollHeight(5000); // a cap eviction lands in the same frame
    scroll.noteContentShrink(before);
    scroll.reconcileScrollRange();
    expect(f.el.scrollTop).toBe(4400);
  });

  it("does not correct a subpixel residual", () => {
    // scrollHeight and clientHeight are integers while scrollTop is fractional,
    // so a correctly reconciled offset can read a fraction past the end. The
    // epsilon keeps its original job; correcting this would write on every
    // shrink pass to move the viewport by half a pixel.
    const f = makeDeferredClampScrollEl(6000, 600);
    scroll = registerForDispose(createScrollController({ scrollEl: f.el }));
    f.userScrollTo(5400);
    Object.defineProperty(f.el, "scrollTop", { value: 5400.6, configurable: true });

    scroll.noteContentShrink(5400.6);
    scroll.reconcileScrollRange();
    expect(f.el.scrollTop).toBe(5400.6); // untouched
  });

  it("arms both questions when the container reconciles only partway", () => {
    // The two questions are independent, which is why neither test returns early
    // on the other: a partial reconciliation moved the offset (so the event it
    // produced is a clamp, not a gesture) AND left it out of range (so a
    // correction is still owed). Answering only the first is what shipped.
    const f = makeDeferredClampScrollEl(85000, 600);
    scroll = registerForDispose(createScrollController({ scrollEl: f.el }));
    f.userScrollTo(84400);

    const before = f.el.scrollTop;
    f.setScrollHeight(600);
    Object.defineProperty(f.el, "scrollTop", { value: 40000, configurable: true });
    scroll.noteContentShrink(before); // moved down, and still illegal

    // The clamp's own event must not disengage follow (question one)...
    f.el.dispatchEvent(new Event("scroll"));
    expect(scroll.isUserScrolledUp()).toBe(false);
    // ...and the offset must still be corrected (question two). Restore a
    // writable property so the correction can land.
    let top = 40000;
    Object.defineProperty(f.el, "scrollTop", {
      get: () => top,
      set: (v: number) => {
        top = Math.max(0, Math.min(v, f.maxTop()));
      },
      configurable: true,
    });
    scroll.reconcileScrollRange();
    expect(f.el.scrollTop).toBe(0);
  });

  it("is a no-op when nothing armed it, so an out-of-band call cannot misfire", () => {
    const f = makeDeferredClampScrollEl(6000, 600);
    scroll = registerForDispose(createScrollController({ scrollEl: f.el }));
    f.userScrollTo(5400);
    f.userScrollTo(2000); // holding, well inside the range
    scroll.reconcileScrollRange();
    expect(f.el.scrollTop).toBe(2000);
    expect(scroll.isUserScrolledUp()).toBe(true);
  });

  it("consumes the arm, so a later pass that strands nothing writes nothing", () => {
    const f = makeDeferredClampScrollEl(85000, 600);
    scroll = registerForDispose(createScrollController({ scrollEl: f.el }));
    f.userScrollTo(84400);

    const before = f.el.scrollTop;
    f.setScrollHeight(600);
    scroll.noteContentShrink(before);
    scroll.reconcileScrollRange();
    expect(f.el.scrollTop).toBe(0);

    // Output resumes and the reader scrolls up to read it. A second call with
    // nothing armed must not drag them back to the tail.
    f.setScrollHeight(6000);
    f.userScrollTo(1000);
    scroll.reconcileScrollRange();
    expect(f.el.scrollTop).toBe(1000);
  });
});

// A scroll event is queued at most once per target per frame
// (https://drafts.csswg.org/cssom-view/#scrolling-events), so a user move and a
// library write can share one. Attached: the intent listeners sit on the window.
interface LiveScroller {
  el: HTMLElement;
  child: HTMLElement;
  writes: number[];
  userScrollTo(top: number): void;
  fire(): void;
  grow(px: number): void;
  setClientHeight(px: number): void;
  maxTop(): number;
}

function makeLiveScroller(scrollHeight: number, clientHeight: number): LiveScroller {
  const el = document.createElement("div");
  const child = document.createElement("div");
  el.appendChild(child);
  document.body.appendChild(el);
  let height = scrollHeight;
  let viewport = clientHeight;
  let top = 0;
  const writes: number[] = [];
  const maxTop = (): number => Math.max(0, height - viewport);
  const clamp = (v: number): number => Math.max(0, Math.min(v, maxTop()));
  Object.defineProperty(el, "scrollHeight", { get: () => height, configurable: true });
  Object.defineProperty(el, "clientHeight", { get: () => viewport, configurable: true });
  Object.defineProperty(el, "scrollTop", {
    get: () => top,
    set: (v: number) => {
      writes.push(v);
      top = clamp(v);
    },
    configurable: true,
  });
  return {
    el,
    child,
    writes,
    userScrollTo(next: number): void {
      top = clamp(next);
    },
    fire(): void {
      el.dispatchEvent(new Event("scroll"));
    },
    grow(px: number): void {
      height += px;
    },
    setClientHeight(px: number): void {
      viewport = px;
      top = clamp(top);
    },
    maxTop,
  };
}

function touch(
  type: "touchstart" | "touchmove" | "touchend",
  target: HTMLElement,
  clientY: number,
): void {
  const t = new Touch({ identifier: 1, target, clientX: 10, clientY });
  const down = type === "touchend" ? [] : [t];
  target.dispatchEvent(
    new TouchEvent(type, {
      bubbles: true,
      cancelable: true,
      touches: down,
      targetTouches: down,
      changedTouches: [t],
    }),
  );
}

function wheel(target: HTMLElement, init: WheelEventInit): void {
  target.dispatchEvent(new WheelEvent("wheel", { bubbles: true, cancelable: true, ...init }));
}

function keydown(target: HTMLElement, key: string): void {
  target.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }));
}

function scrollend(el: HTMLElement): void {
  // An element's scrollend does not bubble: https://drafts.csswg.org/cssom-view/#scrolling-events
  el.dispatchEvent(new Event("scrollend"));
}

// HTML fires it at the document with bubbles=true:
// https://html.spec.whatwg.org/multipage/interaction.html#update-the-visibility-state
function visibilitychange(): void {
  document.dispatchEvent(new Event("visibilitychange", { bubbles: true }));
}

describe("follow classification against the library's own writes", () => {
  let s: LiveScroller | undefined;

  function followAtBottom(): LiveScroller {
    const live = makeLiveScroller(6000, 600);
    s = live;
    scroll = registerForDispose(createScrollController({ scrollEl: live.el }));
    live.userScrollTo(5400);
    live.fire();
    return live;
  }

  afterEach(() => {
    s?.el.remove();
    s = undefined;
    vi.useRealTimers();
  });

  it.each([5, 10, 16])(
    "holds when a %ipx upward move lands in one event with a 17px pin",
    (delta) => {
      const f = followAtBottom();
      f.grow(17);
      scroll.stickToBottom();
      f.userScrollTo(5417 - delta);
      f.fire();

      expect(scroll.isUserScrolledUp()).toBe(true);
      f.grow(17);
      scroll.stickToBottom();
      expect(f.el.scrollTop).toBe(5417 - delta);
    },
  );

  it("keeps following through the echo of its own pin", () => {
    const f = followAtBottom();
    f.grow(17);
    scroll.stickToBottom();
    f.fire();

    expect(scroll.isUserScrolledUp()).toBe(false);
    f.grow(17);
    scroll.stickToBottom();
    expect(f.el.scrollTop).toBe(5434);
  });

  it("classifies a return to the tail that shares one event with an anchor correction", () => {
    const f = followAtBottom();
    f.userScrollTo(5300);
    f.fire();
    scroll.adjustForContentShift(-34);
    f.userScrollTo(5400);
    f.fire();

    expect(scroll.isUserScrolledUp()).toBe(false);
  });

  it("classifies an upward move that shares one event with a restore", () => {
    const f = followAtBottom();
    scroll.restoreView({ top: 3000, following: true });
    f.userScrollTo(2980);
    f.fire();

    expect(scroll.isUserScrolledUp()).toBe(true);
  });

  it("adds up sub-pixel upward steps until they leave a real gap", () => {
    // A precise trackpad on a fractional device-pixel ratio scrolls in steps
    // smaller than the echo tolerance; each one alone is noise, together a move.
    const f = followAtBottom();
    for (const top of [5399.4, 5398.8, 5398.2]) {
      f.userScrollTo(top);
      f.fire();
    }

    expect(scroll.isUserScrolledUp()).toBe(true);
  });

  it("disengages on an upward move the page can see before its event arrives", () => {
    const f = followAtBottom();
    f.userScrollTo(5390);
    f.grow(17);
    const before = f.writes.length;
    scroll.stickToBottom();

    expect(f.writes.length).toBe(before);
    expect(scroll.isUserScrolledUp()).toBe(true);
  });

  it("notifies the position for a user move coalesced with an anchor correction", () => {
    const f = makeLiveScroller(6000, 600);
    s = f;
    let positions = 0;
    scroll = registerForDispose(
      createScrollController({ scrollEl: f.el, onScrollPosition: () => (positions += 1) }),
    );
    f.userScrollTo(5300);
    f.fire();
    positions = 0;
    scroll.adjustForContentShift(-34);
    f.userScrollTo(5000);
    f.fire();

    expect(positions).toBe(1);
  });

  it("owes the next event a notification when a write settled a user move first", () => {
    const f = makeLiveScroller(6000, 600);
    s = f;
    let positions = 0;
    scroll = registerForDispose(
      createScrollController({ scrollEl: f.el, onScrollPosition: () => (positions += 1) }),
    );
    f.userScrollTo(5300);
    f.fire();
    positions = 0;
    f.userScrollTo(5200);
    scroll.adjustForContentShift(-34);
    expect(positions).toBe(0); // never synchronously from inside a write
    f.fire();

    expect(positions).toBe(1);
  });

  it("disengages on an upward wheel before any scroll event", () => {
    const f = followAtBottom();
    wheel(f.child, { deltaY: -10 });

    expect(scroll.isUserScrolledUp()).toBe(true);
    f.grow(17);
    const before = f.writes.length;
    scroll.stickToBottom();
    expect(f.writes.length).toBe(before);
  });

  it("ignores an upward wheel the page cancelled", () => {
    // Mouse tracking cancels the wheel and reports it to the application instead.
    const f = followAtBottom();
    f.child.addEventListener("wheel", (e) => e.preventDefault(), { passive: false });
    wheel(f.child, { deltaY: -10 });

    expect(scroll.isUserScrolledUp()).toBe(false);
  });

  it("ignores a pinch-zoom wheel", () => {
    const f = followAtBottom();
    wheel(f.child, { deltaY: -10, ctrlKey: true });

    expect(scroll.isUserScrolledUp()).toBe(false);
  });

  it("ignores a downward wheel and a wheel outside the container", () => {
    const f = followAtBottom();
    wheel(f.child, { deltaY: 10 });
    wheel(document.body, { deltaY: -10 });

    expect(scroll.isUserScrolledUp()).toBe(false);
  });

  it("ignores an upward wheel when there is nothing above to scroll to", () => {
    const f = makeLiveScroller(600, 600);
    s = f;
    scroll = registerForDispose(createScrollController({ scrollEl: f.el }));
    wheel(f.child, { deltaY: -10 });

    expect(scroll.isUserScrolledUp()).toBe(false);
  });

  it("restores follow when an upward wheel moved nothing within the quiet window", () => {
    vi.useFakeTimers();
    const f = followAtBottom();
    wheel(f.child, { deltaY: -10 });
    f.grow(17);

    vi.advanceTimersByTime(199);
    expect(scroll.isUserScrolledUp()).toBe(true);
    vi.advanceTimersByTime(1);
    expect(scroll.isUserScrolledUp()).toBe(false);
    expect(f.el.scrollTop).toBe(5417);
  });

  it.each(["PageUp", "Home"])("disengages on %s aimed at the page", (key) => {
    followAtBottom();
    keydown(document.body, key);

    expect(scroll.isUserScrolledUp()).toBe(true);
  });

  it("ignores a PageUp the terminal handled", () => {
    // A mapped key is sent to the application, which cancels the event.
    const f = followAtBottom();
    f.child.addEventListener("keydown", (e) => e.preventDefault());
    keydown(f.child, "PageUp");

    expect(scroll.isUserScrolledUp()).toBe(false);
  });

  it("ignores a PageUp aimed at an element outside the container", () => {
    followAtBottom();
    const field = document.createElement("input");
    document.body.appendChild(field);
    keydown(field, "PageUp");
    field.remove();

    expect(scroll.isUserScrolledUp()).toBe(false);
  });

  it("restores follow when a PageUp on the page scrolled some other element", () => {
    vi.useFakeTimers();
    const f = followAtBottom();
    keydown(document.body, "PageUp");
    f.grow(17);

    vi.advanceTimersByTime(199);
    expect(scroll.isUserScrolledUp()).toBe(true);
    vi.advanceTimersByTime(1);
    expect(scroll.isUserScrolledUp()).toBe(false);
    expect(f.el.scrollTop).toBe(5417);
  });

  it("keeps holding when the PageUp scrolled the container", () => {
    vi.useFakeTimers();
    const f = followAtBottom();
    keydown(document.body, "PageUp");
    f.userScrollTo(4900);
    f.fire();
    f.grow(17);
    vi.advanceTimersByTime(200);

    expect(scroll.isUserScrolledUp()).toBe(true);
    expect(f.el.scrollTop).toBe(4900);
  });

  it("does not count an announced shrink as the movement that confirms intent", () => {
    vi.useFakeTimers();
    const f = followAtBottom();
    f.grow(17);
    keydown(document.body, "PageUp");
    f.grow(-100);
    f.userScrollTo(5300);
    scroll.noteContentShrink(5400);
    f.fire();
    vi.advanceTimersByTime(200);

    expect(scroll.isUserScrolledUp()).toBe(false);
    expect(f.el.scrollTop).toBe(5317);
  });

  it("does not count a browser clamp to the bottom as the movement that confirms intent", () => {
    vi.useFakeTimers();
    const f = followAtBottom();
    keydown(document.body, "PageUp");
    f.setClientHeight(700);
    f.fire();
    vi.advanceTimersByTime(200);

    expect(scroll.isUserScrolledUp()).toBe(false);
    f.grow(17);
    scroll.stickToBottom();
    expect(f.el.scrollTop).toBe(5317);
  });

  it("confirms intent by a downward move that stops short of the bottom", () => {
    vi.useFakeTimers();
    const f = followAtBottom();
    f.grow(500);
    keydown(document.body, "PageUp");
    f.userScrollTo(5600);
    f.fire();
    vi.advanceTimersByTime(200);

    expect(scroll.isUserScrolledUp()).toBe(true);
    expect(f.el.scrollTop).toBe(5600);
  });

  it("keeps an explicit restore made while intent was unconfirmed", () => {
    vi.useFakeTimers();
    followAtBottom();
    keydown(document.body, "PageUp");
    scroll.restoreView({ top: 5400, following: false });
    vi.advanceTimersByTime(200);

    expect(scroll.isUserScrolledUp()).toBe(true);
  });

  it("never re-engages a reader who was already holding when the intent came", () => {
    vi.useFakeTimers();
    const f = followAtBottom();
    f.userScrollTo(5000);
    f.fire();
    wheel(f.child, { deltaY: -10 });
    vi.advanceTimersByTime(200);

    expect(scroll.isUserScrolledUp()).toBe(true);
  });

  it("holds as soon as a finger pulls the content down 8px, before any scroll event", () => {
    const f = followAtBottom();
    touch("touchstart", f.child, 100);
    touch("touchmove", f.child, 108);

    expect(scroll.isUserScrolledUp()).toBe(true);
  });

  it("does not hold for a finger movement under 8px", () => {
    const f = followAtBottom();
    touch("touchstart", f.child, 100);
    touch("touchmove", f.child, 107);
    touch("touchend", f.child, 107);

    expect(scroll.isUserScrolledUp()).toBe(false);
  });

  it("restores follow when a finger pull lifted without scrolling the content", () => {
    vi.useFakeTimers();
    const f = followAtBottom();
    touch("touchstart", f.child, 100);
    touch("touchmove", f.child, 110);
    touch("touchend", f.child, 110);
    f.grow(17);

    expect(scroll.isUserScrolledUp()).toBe(true);
    vi.advanceTimersByTime(200);
    expect(scroll.isUserScrolledUp()).toBe(false);
    expect(f.el.scrollTop).toBe(5417);
  });

  it("keeps holding when a finger pull's scroll arrives after the finger lifted", () => {
    vi.useFakeTimers();
    const f = followAtBottom();
    touch("touchstart", f.child, 100);
    touch("touchmove", f.child, 140);
    touch("touchend", f.child, 140);
    vi.advanceTimersByTime(100);
    f.userScrollTo(5360);
    f.fire();
    f.grow(17);
    vi.advanceTimersByTime(200);

    expect(scroll.isUserScrolledUp()).toBe(true);
    expect(f.el.scrollTop).toBe(5360);
  });

  it("stays held through a finger drag up and its momentum", () => {
    const f = followAtBottom();
    touch("touchstart", f.child, 100);
    touch("touchmove", f.child, 140);
    f.userScrollTo(5360);
    f.fire();
    touch("touchend", f.child, 140);
    for (const top of [5300, 5250, 5220]) {
      f.userScrollTo(top);
      f.fire();
    }
    scrollend(f.el);
    f.grow(17);
    const before = f.writes.length;
    scroll.stickToBottom();

    expect(f.writes.length).toBe(before);
    expect(f.el.scrollTop).toBe(5220);
    expect(scroll.isUserScrolledUp()).toBe(true);
  });

  it("re-engages follow when a finger drags back to the bottom, and pins after release", () => {
    const f = followAtBottom();
    f.userScrollTo(4000);
    f.fire();
    touch("touchstart", f.child, 300);
    touch("touchmove", f.child, 200);
    f.userScrollTo(5000);
    f.fire();
    f.userScrollTo(5400);
    f.fire();
    touch("touchend", f.child, 200);
    scrollend(f.el);

    expect(scroll.isUserScrolledUp()).toBe(false);
    f.grow(17);
    scroll.stickToBottom();
    expect(f.el.scrollTop).toBe(5417);
  });

  it("releases the hold at touchend after a tap and performs the pin it skipped", () => {
    const f = followAtBottom();
    touch("touchstart", f.child, 100);
    f.grow(17);
    const before = f.writes.length;
    scroll.stickToBottom();

    expect(f.writes.length).toBe(before);
    touch("touchend", f.child, 100);
    expect(f.el.scrollTop).toBe(5417);
    expect(scroll.isUserScrolledUp()).toBe(false);
  });

  it("leaves follow engaged after a touch that moves and ends at the bottom", () => {
    const f = followAtBottom();
    touch("touchstart", f.child, 100);
    touch("touchmove", f.child, 104);
    f.userScrollTo(5396);
    f.fire();
    touch("touchmove", f.child, 100);
    f.userScrollTo(5400);
    f.fire();
    touch("touchend", f.child, 100);
    scrollend(f.el);

    expect(scroll.isUserScrolledUp()).toBe(false);
    f.grow(17);
    scroll.stickToBottom();
    expect(f.el.scrollTop).toBe(5417);
  });

  it("holds the pin while a touch scrolls and performs it at scrollend", () => {
    const f = followAtBottom();
    touch("touchstart", f.child, 300);
    f.grow(17);
    scroll.stickToBottom();
    f.userScrollTo(5410);
    f.fire();
    touch("touchend", f.child, 290);
    scroll.stickToBottom();

    expect(f.el.scrollTop).toBe(5410);
    scrollend(f.el);
    expect(f.el.scrollTop).toBe(5417);
  });

  it("counts a move a write settled during the press as scrolling, not a tap", () => {
    const f = followAtBottom();
    touch("touchstart", f.child, 300);
    f.grow(17);
    scroll.stickToBottom();
    // The next pin settles the move before its event, which then reads as an echo.
    f.userScrollTo(5410);
    scroll.stickToBottom();
    f.fire();
    touch("touchend", f.child, 290);

    expect(f.el.scrollTop).toBe(5410);
    scrollend(f.el);
    expect(f.el.scrollTop).toBe(5417);
  });

  it("releases on a 200ms quiet timer where scrollend never fires", () => {
    vi.useFakeTimers();
    const f = followAtBottom();
    touch("touchstart", f.child, 300);
    f.grow(17);
    scroll.stickToBottom();
    f.userScrollTo(5410);
    f.fire();
    touch("touchend", f.child, 290);

    vi.advanceTimersByTime(199);
    expect(f.el.scrollTop).toBe(5410);
    vi.advanceTimersByTime(1);
    expect(f.el.scrollTop).toBe(5417);
  });

  it("restarts the quiet timer on every momentum scroll event", () => {
    vi.useFakeTimers();
    const f = followAtBottom();
    touch("touchstart", f.child, 300);
    f.grow(17);
    scroll.stickToBottom();
    f.userScrollTo(5405);
    f.fire();
    touch("touchend", f.child, 290);
    vi.advanceTimersByTime(150);
    f.userScrollTo(5410);
    f.fire();

    vi.advanceTimersByTime(199);
    expect(f.el.scrollTop).toBe(5410);
    vi.advanceTimersByTime(1);
    expect(f.el.scrollTop).toBe(5417);
  });

  it("ends the press when the touched node was removed mid-gesture", () => {
    // The renderer rebuilds live rows in place, and a removed node's events no
    // longer propagate to the window.
    const f = followAtBottom();
    touch("touchstart", f.child, 100);
    f.child.remove();
    f.grow(17);
    scroll.stickToBottom();
    touch("touchend", f.child, 100);

    expect(f.el.scrollTop).toBe(5417);
  });

  it("holds for a finger pull on a node the renderer removed after the touch began", () => {
    vi.useFakeTimers();
    const f = followAtBottom();
    touch("touchstart", f.child, 100);
    f.child.remove();
    touch("touchmove", f.child, 140);
    f.grow(17);
    scroll.stickToBottom();
    const before = f.writes.length;
    touch("touchend", f.child, 140);
    expect(f.writes.length).toBe(before);

    vi.advanceTimersByTime(100);
    f.userScrollTo(5360);
    f.fire();
    vi.advanceTimersByTime(200);
    expect(scroll.isUserScrolledUp()).toBe(true);
    expect(f.el.scrollTop).toBe(5360);
  });

  it("ignores a finger pull the page cancelled", () => {
    const f = followAtBottom();
    const cancel = (e: Event): void => {
      e.preventDefault();
    };
    document.addEventListener("touchmove", cancel, { passive: false });
    touch("touchstart", f.child, 100);
    touch("touchmove", f.child, 140);
    document.removeEventListener("touchmove", cancel);

    expect(scroll.isUserScrolledUp()).toBe(false);
  });

  it("ignores a finger pull whose touch the page cancelled at its start", () => {
    const f = followAtBottom();
    const cancel = (e: Event): void => {
      e.preventDefault();
    };
    document.addEventListener("touchstart", cancel, { passive: false });
    touch("touchstart", f.child, 100);
    document.removeEventListener("touchstart", cancel);
    touch("touchmove", f.child, 140);

    expect(scroll.isUserScrolledUp()).toBe(false);
  });

  it("ignores a pull on a removed node whose own listener cancelled it", () => {
    const f = followAtBottom();
    f.child.addEventListener("touchmove", (e) => e.preventDefault(), { passive: false });
    touch("touchstart", f.child, 100);
    f.child.remove();
    touch("touchmove", f.child, 140);

    expect(scroll.isUserScrolledUp()).toBe(false);
  });

  it("ignores a pull on a removed row's span that the row's listener cancelled", () => {
    const f = followAtBottom();
    const span = document.createElement("span");
    f.child.appendChild(span);
    f.child.addEventListener("touchmove", (e) => e.preventDefault(), { passive: false });
    touch("touchstart", span, 100);
    f.child.remove();
    touch("touchmove", span, 140);

    expect(scroll.isUserScrolledUp()).toBe(false);
  });

  it("holds for a pull on a span whose row the renderer removed", () => {
    const f = followAtBottom();
    const span = document.createElement("span");
    f.child.appendChild(span);
    touch("touchstart", span, 100);
    f.child.remove();
    touch("touchmove", span, 140);

    expect(scroll.isUserScrolledUp()).toBe(true);
  });

  it("adds no listener to the container for a touch that lands on it directly", () => {
    const f = followAtBottom();
    const add = vi.spyOn(f.el, "addEventListener");
    touch("touchstart", f.el, 100);
    touch("touchmove", f.el, 104);
    touch("touchend", f.el, 104);

    expect(add).not.toHaveBeenCalled();
  });

  it("performs the owed pin when a tap directly on the container ends", () => {
    const f = followAtBottom();
    touch("touchstart", f.el, 100);
    f.grow(17);
    scroll.stickToBottom();
    expect(f.el.scrollTop).toBe(5400);

    touch("touchend", f.el, 100);
    expect(f.el.scrollTop).toBe(5417);
  });

  it("ends the press when the last finger to lift began outside the container", () => {
    const f = followAtBottom();
    const outside = document.createElement("div");
    document.body.appendChild(outside);
    const inner = new Touch({ identifier: 1, target: f.child, clientX: 10, clientY: 100 });
    const outer = new Touch({ identifier: 2, target: outside, clientX: 10, clientY: 100 });
    const send = (type: string, target: HTMLElement, down: Touch[], changed: Touch): void => {
      target.dispatchEvent(
        new TouchEvent(type, { bubbles: true, touches: down, changedTouches: [changed] }),
      );
    };
    send("touchstart", f.child, [inner], inner);
    send("touchstart", outside, [inner, outer], outer);
    f.grow(17);
    scroll.stickToBottom();
    send("touchend", f.child, [outer], inner);
    expect(f.el.scrollTop).toBe(5400);

    send("touchend", outside, [], outer);
    outside.remove();
    expect(f.el.scrollTop).toBe(5417);
  });

  it("ends the press when a page listener stops the touchend", () => {
    const stop = (e: Event): void => {
      e.stopPropagation();
    };
    document.addEventListener("touchend", stop);
    const f = followAtBottom();
    touch("touchstart", f.child, 100);
    f.grow(17);
    scroll.stickToBottom();
    touch("touchend", f.child, 100);
    document.removeEventListener("touchend", stop);

    expect(f.el.scrollTop).toBe(5417);
  });

  it("keeps following when a drag toward the tail stops short of output that grew during the press", () => {
    const f = followAtBottom();
    touch("touchstart", f.child, 300);
    f.grow(102);
    scroll.stickToBottom();
    touch("touchmove", f.child, 280);
    f.userScrollTo(5430);
    f.fire();
    touch("touchend", f.child, 280);
    scrollend(f.el);

    expect(scroll.isUserScrolledUp()).toBe(false);
    expect(f.el.scrollTop).toBe(5502);
  });

  it("keeps following when a flick toward the tail ends short of output that grew during it", () => {
    vi.useFakeTimers();
    const f = followAtBottom();
    touch("touchstart", f.child, 300);
    touch("touchmove", f.child, 250);
    for (const top of [5408, 5416]) {
      f.grow(17);
      scroll.stickToBottom();
      f.userScrollTo(top);
      f.fire();
    }
    touch("touchend", f.child, 250);
    for (const top of [5424, 5432, 5440, 5448]) {
      f.grow(17);
      scroll.stickToBottom();
      f.userScrollTo(top);
      f.fire();
    }
    vi.advanceTimersByTime(200);

    expect(scroll.isUserScrolledUp()).toBe(false);
    expect(f.el.scrollTop).toBe(5502);
  });

  it("releases a press the page was hidden in the middle of", () => {
    const f = followAtBottom();
    touch("touchstart", f.child, 100);
    f.grow(17);
    scroll.stickToBottom();
    expect(f.el.scrollTop).toBe(5400);

    vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    visibilitychange();
    expect(f.el.scrollTop).toBe(5417);
  });

  it("releases a hidden page's press even when a page listener stops the event", () => {
    const stop = (e: Event): void => {
      e.stopPropagation();
    };
    document.addEventListener("visibilitychange", stop);
    const f = followAtBottom();
    touch("touchstart", f.child, 100);
    f.grow(17);
    scroll.stickToBottom();

    vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    visibilitychange();
    document.removeEventListener("visibilitychange", stop);
    expect(f.el.scrollTop).toBe(5417);
  });

  it("keeps the press when the page becomes visible", () => {
    const f = followAtBottom();
    touch("touchstart", f.child, 100);
    f.grow(17);
    scroll.stickToBottom();
    visibilitychange();

    expect(f.el.scrollTop).toBe(5400);
  });

  it("keeps the tail across a soft keyboard opening and closing", () => {
    const f = followAtBottom();
    f.setClientHeight(300);
    scroll.stickToBottom();
    expect(f.el.scrollTop).toBe(5700);
    f.fire();

    f.setClientHeight(600);
    f.fire();
    scroll.stickToBottom();
    expect(f.el.scrollTop).toBe(5400);
    expect(scroll.isUserScrolledUp()).toBe(false);
  });

  it("lands on the tail when the keyboard a tap raised resizes during the hold", () => {
    const f = followAtBottom();
    touch("touchstart", f.child, 100);
    f.setClientHeight(300);
    scroll.stickToBottom();

    expect(f.el.scrollTop).toBe(5400);
    touch("touchend", f.child, 100);
    expect(f.el.scrollTop).toBe(5700);
  });

  it("holds the pin while the scrollbar is pressed and performs it at release", () => {
    const f = followAtBottom();
    f.el.style.width = "100px";
    f.el.style.height = "50px";
    Object.defineProperty(f.el, "clientWidth", { get: () => 80, configurable: true });
    const box = f.el.getBoundingClientRect();
    f.el.dispatchEvent(
      new PointerEvent("pointerdown", {
        pointerType: "mouse",
        bubbles: true,
        clientX: box.left + 90,
        clientY: box.top + 10,
      }),
    );
    f.grow(17);
    scroll.stickToBottom();

    expect(f.el.scrollTop).toBe(5400);
    window.dispatchEvent(new PointerEvent("pointerup", { pointerType: "mouse" }));
    expect(f.el.scrollTop).toBe(5417);
  });

  it("does not hold for a mouse press on the content beside the scrollbar", () => {
    const f = followAtBottom();
    f.el.style.width = "100px";
    f.el.style.height = "50px";
    Object.defineProperty(f.el, "clientWidth", { get: () => 80, configurable: true });
    const box = f.el.getBoundingClientRect();
    f.el.dispatchEvent(
      new PointerEvent("pointerdown", {
        pointerType: "mouse",
        bubbles: true,
        clientX: box.left + 70,
        clientY: box.top + 10,
      }),
    );
    f.grow(17);
    scroll.stickToBottom();

    expect(f.el.scrollTop).toBe(5417);
  });

  it("dispose removes every window listener it added, with the same capture flag", () => {
    const capture = (o: unknown): boolean =>
      typeof o === "boolean" ? o : ((o as { capture?: boolean } | undefined)?.capture ?? false);
    const add = vi.spyOn(window, "addEventListener");
    const remove = vi.spyOn(window, "removeEventListener");
    const f = makeLiveScroller(6000, 600);
    s = f;
    const controller = createScrollController({ scrollEl: f.el });
    const added = add.mock.calls.map((c) => [c[0], c[1], capture(c[2])]);
    controller.dispose();
    const removed = remove.mock.calls.map((c) => [c[0], c[1], capture(c[2])]);

    expect(added.length).toBeGreaterThan(0);
    expect(removed).toHaveLength(added.length);
    expect(removed).toEqual(expect.arrayContaining(added));
  });

  it("dispose releases the node a touch in progress landed on", () => {
    const f = followAtBottom();
    const add = vi.spyOn(f.child, "addEventListener");
    const remove = vi.spyOn(f.child, "removeEventListener");
    touch("touchstart", f.child, 100);
    scroll.dispose();

    expect(add).toHaveBeenCalled();
    expect(remove.mock.calls.map((c) => [c[0], c[1]])).toEqual(
      add.mock.calls.map((c) => [c[0], c[1]]),
    );
  });

  it("still classifies scroll events for a container with no window", () => {
    const doc = document.implementation.createHTMLDocument("");
    const el = doc.createElement("div");
    let top = 0;
    Object.defineProperty(el, "scrollHeight", { get: () => 1000, configurable: true });
    Object.defineProperty(el, "clientHeight", { get: () => 300, configurable: true });
    Object.defineProperty(el, "scrollTop", {
      get: () => top,
      set: (v: number) => {
        top = v;
      },
      configurable: true,
    });
    scroll = registerForDispose(createScrollController({ scrollEl: el }));
    top = 700;
    el.dispatchEvent(new Event("scroll"));
    top = 600;
    el.dispatchEvent(new Event("scroll"));

    expect(scroll.isUserScrolledUp()).toBe(true);
  });
});
