// @vitest-environment happy-dom
//
// Brick-4 scroll controller. happy-dom has no real layout, so scrollHeight /
// clientHeight are overridden to drive the follow/hold state machine
// deterministically: following is derived purely from scroll position, with a
// small bottom tolerance, and stickToBottom only pins when following.

import { describe, it, expect, beforeEach } from "vitest";
import * as scroll from "./scroll.js";

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
    scroll.init({ scrollEl: el, onUserScrollChange: (up) => changes.push(up) });
  });

  it("starts in the following state", () => {
    expect(scroll.isUserScrolledUp()).toBe(false);
  });

  it("flips to holding when the user scrolls up past the tolerance, and back", () => {
    scrollTo(el, 0); // distance from bottom = 700 -> holding
    expect(scroll.isUserScrolledUp()).toBe(true);
    expect(changes).toEqual([true]);
    scrollTo(el, 700); // distance = 0 -> following
    expect(scroll.isUserScrolledUp()).toBe(false);
    expect(changes).toEqual([true, false]);
  });

  it("treats within-tolerance as following (24px)", () => {
    scrollTo(el, 680); // distance = 20 (<= 24) -> following
    expect(scroll.isUserScrolledUp()).toBe(false);
    scrollTo(el, 669); // distance = 31 (> 24) -> holding
    expect(scroll.isUserScrolledUp()).toBe(true);
  });

  it("stickToBottom pins to the bottom while following", () => {
    expect(el.scrollTop).toBe(0);
    scroll.stickToBottom();
    expect(el.scrollTop).toBe(1000); // pinned to scrollHeight
  });

  it("stickToBottom does nothing while holding (does not yank the reader)", () => {
    scrollTo(el, 100); // holding
    scroll.stickToBottom();
    expect(el.scrollTop).toBe(100); // unchanged
  });

  it("scrollToBottom forces the bottom and re-engages following", () => {
    scrollTo(el, 0); // holding
    expect(scroll.isUserScrolledUp()).toBe(true);
    scroll.scrollToBottom();
    expect(el.scrollTop).toBe(1000);
    expect(scroll.isUserScrolledUp()).toBe(false);
  });
});
