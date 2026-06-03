// Wire types matching the Go vt package's wire format.

/**
 * A single styled run of text within a terminal row. The Go server emits these
 * as flat arrays per row; the renderer reconstructs cells from them.
 */
export interface WireRun {
  /** UTF-8 text content of this run. */
  t: string;
  /** Foreground color (palette index or RGB packed); -1 means default. */
  f?: number;
  /** Background color (palette index or RGB packed); -1 means default. */
  b?: number;
  /** Underline color (palette index or RGB packed); -1 means default. */
  uc?: number;
  /**
   * Style attribute bitflags:
   * 1=bold, 2=italic, 4=underline, 8=inverse, 16=strike, 32=dim, 64=hidden,
   * 128=blink, 256=overline, 512=double-underline.
   */
  a?: number;
  /** OSC 8 hyperlink URI (empty/absent means no link). */
  u?: string;
}

/**
 * Full or incremental screen update from the server. `changed` lists row
 * indices in `rows` that actually changed since the last frame; the renderer
 * only re-paints those rows.
 */
export interface ScreenMessage {
  type: "screen";
  /** All rows of the visible screen (length = screen height). */
  rows: WireRun[][];
  /** Cursor position as [row, col], zero-indexed. */
  cursor: [number, number];
  /** Indices into `rows` that changed; an empty array means cursor-only update. */
  changed: number[];
  /** DECSCUSR cursor style (0-6); 0 = default block. */
  cursorStyle?: number;
  /** True if the cursor is currently hidden (DECTCEM off). */
  cursorHidden?: boolean;
  /** True if the cursor is currently blinking. */
  cursorBlink?: boolean;
  /** True if the server emitted a BEL since the last screen update. */
  bell?: boolean;
  /** Server-confirmed bytesReceived for the input ACK protocol. */
  inputAck?: number;
}

/**
 * A batch of lines that scrolled off the top of the visible screen. The
 * renderer appends these to the scrollback buffer.
 */
export interface ScrollMessage {
  type: "scroll";
  /** Lines that scrolled out (in order, oldest to newest). */
  lines: WireRun[][];
  /** Server-confirmed bytesReceived for the input ACK protocol. */
  inputAck?: number;
}

/**
 * Server acknowledgement of a client `resume` control message; tells the
 * client how many input bytes the server already saw so the client can
 * replay only what was missed.
 */
export interface ResumeAckMessage {
  type: "resumeAck";
  /** Bytes the server received from this session before the resume. */
  received: number;
  /**
   * Server boot-time nanoseconds since unix epoch. Optional for back-compat
   * with pre-CONN-01 server builds (which omit it).
   */
  serverEpoch?: number;
}

/**
 * Snapshot of the server's terminal mode flags. The client updates its input
 * encoding behavior (keyboard, mouse, paste) based on these.
 */
export interface ModesMessage {
  type: "modes";
  /** DEC 2004: paste content is wrapped with ESC[200~ ... ESC[201~. */
  bracketedPaste: boolean;
  /** DECCKM: cursor keys send ESC O instead of ESC [. */
  applicationCursor: boolean;
  /** DECKPAM: keypad keys send application-mode sequences. */
  applicationKeypad: boolean;
  /** DEC 1006: mouse coordinates are encoded in SGR (CSI <) form. */
  mouseSGR: boolean;
  /** DEC 1004: focus in/out events are reported as ESC[I / ESC[O. */
  focusReporting: boolean;
  /** DEC 5: screen is in reverse-video mode. */
  reverseVideo: boolean;
  /** Mouse tracking: 0=off, 1000=normal, 1002=button-event, 1003=any-event. */
  mouseMode: number;
  /** Server-confirmed bytesReceived for the input ACK protocol. */
  inputAck?: number;
}

/**
 * Window title set by the server (OSC 0/1/2). The client typically reflects
 * this into `document.title`.
 */
export interface TitleMessage {
  type: "title";
  /** New window title text. */
  title: string;
  /** Server-confirmed bytesReceived for the input ACK protocol. */
  inputAck?: number;
}

/** Discriminated union of all messages the server can send to the client. */
export type ServerMessage =
  | ScreenMessage
  | ScrollMessage
  | ResumeAckMessage
  | ModesMessage
  | TitleMessage;

/**
 * Discriminated union of all control messages the client can send to the
 * server (multiplexed alongside raw input bytes; see the wire protocol notes).
 */
export type ControlMessage =
  | { type: "resize"; cols: number; rows: number }
  | { type: "resume"; sessionId: string; sentBytes: number; scrollbackHave: number };
