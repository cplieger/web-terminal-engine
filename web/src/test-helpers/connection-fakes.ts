import type { ConnectionOptions } from "../connection.js";
import { createModeState } from "../modes.js";

export type FakeRenderer = ConnectionOptions["renderer"];

/** A renderer holding nothing: `getReplayBoundary` -1, `replayMaxForResume` 5000, no-ops otherwise. */
export function fakeRenderer(overrides: Partial<FakeRenderer> = {}): FakeRenderer {
  return {
    getReplayBoundary: () => -1,
    replayMaxForResume: () => 5000,
    applyResumeTransition: () => undefined,
    noteResumeBounds: () => undefined,
    handleHistoryReply: () => undefined,
    noteSolicited: () => undefined,
    clearSolicited: () => undefined,
    maybeFetchHistory: () => undefined,
    ...overrides,
  };
}

/** `renderer`, `modes` and `mouse` for a `createConnection` call; spread the callbacks beside them. */
export function connectionDeps(
  renderer: Partial<FakeRenderer> = {},
): Pick<ConnectionOptions, "renderer" | "modes" | "mouse"> {
  return {
    renderer: fakeRenderer(renderer),
    modes: createModeState(),
    mouse: { resyncGesture: () => undefined },
  };
}
