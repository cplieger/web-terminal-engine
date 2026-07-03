// Tests for the session status-stream client (connectStatusStream).
//
// The client is a thin EventSource wrapper, so the tests inject a fake
// EventSourceLike via the `make` parameter and drive events by hand. Behaviors
// tested:
// 1. A well-formed "message" event is parsed and fanned out to onStatus with
//    every field (id/status/title/createdAt/removed) preserved.
// 2. A malformed "message" frame (invalid JSON) is skipped without throwing and
//    without calling onStatus; a later valid frame still fans out.
// 3. onOpen fires on every "open" event (initial connect and each reconnect),
//    so the consumer can resync after a gap.
// 4. onError fires on an "error" event.
// 5. close() closes the underlying EventSource exactly once.
// 6. Optional callbacks (onOpen/onError) absent: no listener is registered and
//    events do not throw.

import { describe, it, expect, vi } from "vitest";

import {
  connectStatusStream,
  type EventSourceLike,
  type SessionStatus,
} from "./status-stream.js";

// FakeEventSource records registered listeners and lets a test dispatch events
// synchronously. It mirrors only the surface connectStatusStream uses.
class FakeEventSource implements EventSourceLike {
  readonly url: string;
  closed = 0;
  private readonly listeners = new Map<string, ((event: MessageEvent) => void)[]>();

  constructor(url: string) {
    this.url = url;
  }

  addEventListener(type: string, listener: (event: MessageEvent) => void): void {
    const list = this.listeners.get(type) ?? [];
    list.push(listener);
    this.listeners.set(type, list);
  }

  close(): void {
    this.closed++;
  }

  // emit dispatches a MessageEvent-shaped payload to every listener for `type`.
  emit(type: string, data?: string): void {
    for (const l of this.listeners.get(type) ?? []) {
      l({ data } as MessageEvent);
    }
  }

  hasListener(type: string): boolean {
    return (this.listeners.get(type)?.length ?? 0) > 0;
  }
}

// mountFake wires a FakeEventSource into connectStatusStream and returns both so
// a test can drive events and assert on the wrapper.
function mountFake(cb: Parameters<typeof connectStatusStream>[1]) {
  let fake: FakeEventSource | undefined;
  const stream = connectStatusStream("/api/sessions/events", cb, (url) => {
    fake = new FakeEventSource(url);
    return fake;
  });
  if (!fake) {
    throw new Error("factory not invoked");
  }
  return { fake, stream };
}

const sample: SessionStatus = {
  id: "abc123",
  status: "input",
  title: "kiro-cli",
  createdAt: "2026-07-01T12:00:00Z",
  removed: false,
};

describe("connectStatusStream", () => {
  it("parses a well-formed message and fans out every field", () => {
    const onStatus = vi.fn();
    const { fake } = mountFake({ onStatus });

    fake.emit("message", JSON.stringify(sample));

    expect(onStatus).toHaveBeenCalledTimes(1);
    expect(onStatus).toHaveBeenCalledWith(sample);
  });

  it("passes the factory the requested path", () => {
    const { fake } = mountFake({ onStatus: vi.fn() });
    expect(fake.url).toBe("/api/sessions/events");
  });

  it("skips a malformed frame without throwing, then delivers the next valid one", () => {
    const onStatus = vi.fn();
    const { fake } = mountFake({ onStatus });

    expect(() => fake.emit("message", "{not json")).not.toThrow();
    expect(onStatus).not.toHaveBeenCalled();

    fake.emit("message", JSON.stringify(sample));
    expect(onStatus).toHaveBeenCalledTimes(1);
    expect(onStatus).toHaveBeenCalledWith(sample);
  });

  it("fires onOpen on every open event (connect and each reconnect)", () => {
    const onOpen = vi.fn();
    const { fake } = mountFake({ onStatus: vi.fn(), onOpen });

    fake.emit("open");
    fake.emit("open");

    expect(onOpen).toHaveBeenCalledTimes(2);
  });

  it("fires onError on an error event", () => {
    const onError = vi.fn();
    const { fake } = mountFake({ onStatus: vi.fn(), onError });

    fake.emit("error");

    expect(onError).toHaveBeenCalledTimes(1);
  });

  it("close() closes the underlying EventSource", () => {
    const { fake, stream } = mountFake({ onStatus: vi.fn() });

    stream.close();

    expect(fake.closed).toBe(1);
  });

  it("registers no open/error listener when the callbacks are absent", () => {
    const { fake } = mountFake({ onStatus: vi.fn() });

    expect(fake.hasListener("open")).toBe(false);
    expect(fake.hasListener("error")).toBe(false);
    // A stray open/error event must not throw with no handler registered.
    expect(() => fake.emit("open")).not.toThrow();
    expect(() => fake.emit("error")).not.toThrow();
  });
});
