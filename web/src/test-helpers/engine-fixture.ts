import type { ConnectionCallbacks } from "../connection.js";
import {
  createTerminalEngine,
  type TerminalEngine,
  type TerminalEngineOptions,
} from "../terminal.js";
import { registerForDispose, unregisterForDispose } from "./dispose-registry.js";

export { registerForDispose } from "./dispose-registry.js";

/** Everything `createTerminalEngine` takes except the elements the fixture builds. */
export interface EngineFixtureOptions extends Omit<
  TerminalEngineOptions,
  "output" | "termWrap" | "callbacks"
> {
  /** Merged over no-op callbacks (`computeSize` answers 80x24). */
  callbacks?: Partial<ConnectionCallbacks>;
}

export interface EngineFixture {
  engine: TerminalEngine;
  output: HTMLDivElement;
  termWrap: HTMLDivElement;
  /** Disposes the engine and removes the elements; the afterEach calls it otherwise. */
  dispose(): void;
}

/** No-op callbacks a test overrides member by member. */
export function noopCallbacks(): ConnectionCallbacks {
  return {
    onMessage: () => undefined,
    onOpen: () => undefined,
    onClose: () => undefined,
    computeSize: () => ({ cols: 80, rows: 24 }),
  };
}

/** Builds `<div class="term"><div class="term-output"></div></div>` under `document.body`. */
export function appendTerminalElements(): { termWrap: HTMLDivElement; output: HTMLDivElement } {
  const termWrap = document.createElement("div");
  termWrap.className = "term";
  const output = document.createElement("div");
  output.className = "term-output";
  termWrap.appendChild(output);
  document.body.appendChild(termWrap);
  return { termWrap, output };
}

/** One engine over a fresh `.term`/`.term-output` pair, disposed by the afterEach. */
export function createEngineFixture(opts: EngineFixtureOptions = {}): EngineFixture {
  const { callbacks, ...rest } = opts;
  const { termWrap, output } = appendTerminalElements();
  let engine: TerminalEngine;
  try {
    engine = createTerminalEngine({
      output,
      termWrap,
      callbacks: { ...noopCallbacks(), ...callbacks },
      ...rest,
    });
  } catch (err) {
    termWrap.remove();
    throw err;
  }
  const fixture: EngineFixture = {
    engine,
    output,
    termWrap,
    dispose(): void {
      unregisterForDispose(fixture);
      engine.dispose();
      termWrap.remove();
    },
  };
  return registerForDispose(fixture);
}
