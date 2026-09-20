import { type Connection, type ConnectionCallbacks, createConnection } from "./connection.js";
import { createModeState, type ModeSnapshot, type ModeState } from "./modes.js";
import { createMouseController, type MouseController } from "./mouse.js";
import { createRenderer, type Renderer } from "./render.js";
import { createScrollController, type ScrollController } from "./scroll.js";

/** What `createTerminalEngine` binds to and forwards to its five parts. */
export interface TerminalEngineOptions {
  /** Inner element that receives the row children. */
  output: HTMLElement;
  /** Outer scroll container; also the mouse listeners' element. */
  termWrap: HTMLElement;
  /**
   * The consumer's connection callbacks. `onMessage` receives every frame after
   * the engine has routed `screen`, `scroll` and `modes` frames to its renderer.
   */
  callbacks: ConnectionCallbacks;
  /** WebSocket endpoint path (default "/ws"). */
  wsPath?: string;
  /** The `sessionStorage` key of the unmanaged session id; distinct per engine on one page. */
  sessionIdKey?: string;
  /** Retained-line cap for the renderer's implicit store; see `RendererOptions.maxLines`. */
  maxLines?: number;
  onCursorMove?: () => void;
  onUserScrollChange?: (scrolledUp: boolean) => void;
  /** Initial mode state (default `POWER_ON_MODES`). */
  initialModes?: Readonly<ModeSnapshot>;
}

/** One terminal: a renderer, a scroll controller, a connection, a mouse controller and a mode state. */
export interface TerminalEngine {
  readonly renderer: Renderer;
  readonly scroll: ScrollController;
  readonly connection: Connection;
  readonly mouse: MouseController;
  readonly modes: ModeState;
  /** Disposes the five parts in reverse build order; each part is idempotent, so this is too. */
  dispose(): void;
}

/**
 * Composes the five factories into one engine that shares nothing with another
 * engine on the same page. It does not connect and does not measure fonts: call
 * `engine.renderer.updateFontMetrics()` when the font is ready and
 * `engine.connection.connect()` to start. Throws, holding nothing, when a part
 * cannot be built.
 */
export function createTerminalEngine(opts: TerminalEngineOptions): TerminalEngine {
  // `renderer` and `connection` are assigned below; every closure that reads them
  // runs from a DOM event or a socket message, none of which can fire before this
  // function returns.
  let renderer: Renderer;
  let connection: Connection;
  const built: { dispose(): void }[] = [];
  let modes: ModeState;
  let scroll: ScrollController;
  let mouse: MouseController;
  try {
    modes = createModeState(opts.initialModes);
    built.push(modes);
    scroll = createScrollController({
      scrollEl: opts.termWrap,
      ...(opts.onUserScrollChange === undefined
        ? {}
        : { onUserScrollChange: opts.onUserScrollChange }),
      onScrollPosition: () => {
        renderer.handleScrollPosition();
      },
    });
    built.push(scroll);
    renderer = createRenderer({
      output: opts.output,
      termWrap: opts.termWrap,
      scroll,
      modes,
      ...(opts.onCursorMove === undefined ? {} : { onCursorMove: opts.onCursorMove }),
      ...(opts.maxLines === undefined ? {} : { maxLines: opts.maxLines }),
      requestHistory: (from, max) => connection.requestHistory(from, max),
      historyBudget: () => connection.historyBudget(),
    });
    built.push(renderer);
    mouse = createMouseController({
      modes,
      termElement: () => opts.termWrap,
      gridElement: () => opts.output,
      cellSize: () => renderer.cellSize(),
      gridSize: () => renderer.gridSize(),
      sendReport: (data) => connection.sendEphemeral(data),
    });
    built.push(mouse);
    const consumer = opts.callbacks;
    const routed: ConnectionCallbacks = {
      ...consumer,
      onMessage(msg) {
        if (msg.type === "screen") {
          renderer.handleScreen(msg);
        } else if (msg.type === "scroll") {
          renderer.handleScroll(msg);
        } else if (msg.type === "modes") {
          renderer.updateReverseVideo();
        }
        consumer.onMessage(msg);
      },
    };
    // The last acquisition: nothing after it can throw, so the connection needs
    // no entry in `built`.
    connection = createConnection({
      renderer,
      modes,
      mouse,
      callbacks: routed,
      ...(opts.wsPath === undefined ? {} : { wsPath: opts.wsPath }),
      ...(opts.sessionIdKey === undefined ? {} : { sessionIdKey: opts.sessionIdKey }),
    });
  } catch (err) {
    for (const part of built.reverse()) {
      part.dispose();
    }
    throw err;
  }
  return {
    renderer,
    scroll,
    connection,
    mouse,
    modes,
    dispose(): void {
      connection.dispose();
      mouse.dispose();
      renderer.dispose();
      scroll.dispose();
      modes.dispose();
    },
  };
}
