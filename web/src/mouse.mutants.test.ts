// The mouse controller's two DOM-contract obligations no behavioural test can
// see, asserted at the registration: the wheel listener's `passive: false`,
// which is what makes its unconditional preventDefault legal (a passive
// listener's preventDefault is ignored with a console warning and the page
// scrolls behind the terminal; xterm.js swallows the gesture the same way), and
// the disposer's detach, since nulling the handler already stops every report,
// so a dropped detach leaves four listeners on a released element with nothing
// to show it.

import { describe, it, expect, beforeEach, vi } from "vitest";

import { createModeState, POWER_ON_MODES } from "./modes.js";
import { createMouseController, type MouseInputHandler } from "./mouse.js";

const modes = createModeState();

/** Every listener the constructor attaches, in the order it attaches them. */
const ATTACHED = ["mousedown", "mouseup", "mousemove", "wheel"] as const;

beforeEach(() => {
  modes.applySnapshot(POWER_ON_MODES);
});

function setup(): { term: HTMLDivElement; sent: string[]; handler: MouseInputHandler } {
  const term = document.createElement("div");
  const sent: string[] = [];
  return {
    term,
    sent,
    handler: {
      sendReport: (data) => {
        sent.push(data);
        return true;
      },
      cellSize: () => ({ width: 8, height: 16 }),
      termElement: () => term,
    },
  };
}

describe("mouse: the wheel listener's passive flag", () => {
  it("registers the wheel listener as NON-passive so preventDefault holds", () => {
    const { term, handler } = setup();
    const spy = vi.spyOn(term, "addEventListener");

    const controller = createMouseController({ ...handler, modes });

    expect(spy).toHaveBeenCalledWith("wheel", expect.any(Function), { passive: false });
    controller.dispose();
    spy.mockRestore();
  });

  it("registers the other three listeners with no options at all", () => {
    // The control for the case above: the flag is on the wheel listener
    // specifically, because the wheel is the only gesture this module cancels.
    const { term, handler } = setup();
    const spy = vi.spyOn(term, "addEventListener");

    const controller = createMouseController({ ...handler, modes });

    for (const type of ATTACHED) {
      if (type === "wheel") {
        continue;
      }
      expect(spy).toHaveBeenCalledWith(type, expect.any(Function));
    }
    controller.dispose();
    spy.mockRestore();
  });
});

describe("mouse: dispose detaches", () => {
  it("removes every listener it attached from the element", () => {
    // Silence is not detachment. The module nulls its handler on dispose, so
    // every listener that stays behind reports nothing and looks correct; what
    // it does instead is keep the element (and the closure holding the previous
    // consumer) alive for as long as the node is referenced.
    const { term, handler } = setup();
    const removed = vi.spyOn(term, "removeEventListener");

    const controller = createMouseController({ ...handler, modes });
    controller.dispose();

    for (const type of ATTACHED) {
      expect(removed).toHaveBeenCalledWith(type, expect.any(Function));
    }
    expect(removed).toHaveBeenCalledTimes(ATTACHED.length);
    removed.mockRestore();
  });

  it("detaches nothing twice when dispose is called again", () => {
    // dispose is documented idempotent, and detach() is what has to be
    // idempotent for that to hold.
    const { term, handler } = setup();
    const controller = createMouseController({ ...handler, modes });
    const removed = vi.spyOn(term, "removeEventListener");

    controller.dispose();
    controller.dispose();

    expect(removed).toHaveBeenCalledTimes(ATTACHED.length);
    removed.mockRestore();
  });
});
