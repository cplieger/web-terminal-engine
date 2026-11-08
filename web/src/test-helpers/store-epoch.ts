import type { LineStore } from "../store.js";

/**
 * Apply a bounds-less, non-paging resumeAck reporting `epoch` (null: none), the
 * way a server tells a store which process its content belongs to. On a store
 * that never saw a paging ack it changes nothing but the epoch.
 */
export function ackEpoch(store: LineStore, epoch: number | null): boolean {
  return store.applyResumeAck({
    serverEpoch: epoch,
    committed: null,
    serverOldest: null,
    paging: false,
    sentHaveThrough: -1,
    sentReplayMax: null,
    viewportAbs: -1,
    following: true,
  });
}
