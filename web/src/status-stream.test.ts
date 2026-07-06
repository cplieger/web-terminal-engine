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
// 6. Optional callbacks absent: the module ALWAYS registers both the OPEN
//    listener (to reset reconnect backoff on every successful reopen) and the
//    ERROR listener (to drive its own reconnect); only the onOpen callback
//    inside the open listener is gated. A stray event does not throw.
// 7. On a permanent close (readyState CLOSED) the module re-establishes the
//    stream after a capped backoff, fires onOpen on the reopen, and close()
//    cancels a pending reconnect.

import { describe, it, expect, vi } from "vitest";

import { connectStatusStream, type EventSourceLike, type SessionStatus } from "./status-stream.js";

// FakeEventSource records registered listeners and lets a test dispatch events
// synchronously. It mirrors only the surface connectStatusStream uses.
class FakeEventSource implements EventSourceLike {
  readonly url: string;
  closed = 0;
  readyState = 0; // 0 CONNECTING, 1 OPEN, 2 CLOSED; a test sets 2 to drive reconnect
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

  it("always registers open+error listeners; gates only the onOpen callback", () => {
    const { fake } = mountFake({ onStatus: vi.fn() });

    // The open listener is ALWAYS registered so it can reset the reconnect
    // backoff on every successful (re)open; only the onOpen callback inside it
    // is gated on the consumer supplying one.
    expect(fake.hasListener("open")).toBe(true);
    // The module always listens for errors so it can re-establish a permanent
    // close even when the consumer supplies no onError callback.
    expect(fake.hasListener("error")).toBe(true);
    // A stray open/error event must not throw with no consumer callback set;
    // readyState stays CONNECTING (0), so no reconnect is scheduled either.
    expect(() => fake.emit("open")).not.toThrow();
    expect(() => fake.emit("error")).not.toThrow();
  });

  it("re-establishes the stream on a permanent close and cancels reconnect on close()", () => {
    vi.useFakeTimers();
    // The reconnect delay is now jittered (base + Math.random()*250, matching
    // the WS reconnect). Pin Math.random to 0.5 so the first attempt's delay is
    // exactly 500 + 0.5*250 = 625ms and the test stays deterministic.
    const randomSpy = vi.spyOn(Math, "random").mockReturnValue(0.5);
    const onOpen = vi.fn();
    const fakes: FakeEventSource[] = [];
    const stream = connectStatusStream(
      "/api/sessions/events",
      { onStatus: vi.fn(), onOpen },
      (url) => {
        const f = new FakeEventSource(url);
        fakes.push(f);
        return f;
      },
    );

    // Initial connect opens exactly one source.
    expect(fakes).toHaveLength(1);
    fakes[0]!.emit("open");
    expect(onOpen).toHaveBeenCalledTimes(1);

    // A permanent close (readyState CLOSED) schedules a reconnect after the
    // jittered backoff (625ms with Math.random pinned to 0.5).
    fakes[0]!.readyState = 2;
    fakes[0]!.emit("error");
    expect(fakes).toHaveLength(1); // not yet: waits for the backoff delay
    vi.advanceTimersByTime(625);
    expect(fakes).toHaveLength(2); // re-established

    // The reopen fires onOpen again so the consumer resyncs after the gap.
    fakes[1]!.emit("open");
    expect(onOpen).toHaveBeenCalledTimes(2);

    // close() cancels any pending reconnect and does not reopen.
    fakes[1]!.readyState = 2;
    fakes[1]!.emit("error");
    stream.close();
    vi.advanceTimersByTime(10_000);
    expect(fakes).toHaveLength(2);
    expect(fakes[1]!.closed).toBe(1);

    randomSpy.mockRestore();
    vi.useRealTimers();
  });
});
