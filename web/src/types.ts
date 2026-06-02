// Wire types matching the Go vt package's wire format.

export interface WireRun {
  t: string;
  f?: number; // -1 = default fg
  b?: number; // -1 = default bg
  uc?: number; // -1 = default underline color
  a?: number; // bit flags: 1=bold, 2=italic, 4=underline, 8=inverse, 16=strike, 32=dim, 64=hidden, 128=blink, 256=overline, 512=double-underline
  u?: string; // OSC 8 hyperlink URI (empty/absent = no link)
}

export interface ScreenMessage {
  type: "screen";
  rows: WireRun[][];
  cursor: [number, number];
  changed: number[];
  cursorStyle?: number; // 0-6: DECSCUSR values
  cursorHidden?: boolean;
  cursorBlink?: boolean;
  bell?: boolean;
  inputAck?: number; // server-confirmed bytesReceived for this session
}

export interface ScrollMessage {
  type: "scroll";
  lines: WireRun[][];
  inputAck?: number;
}

export interface ResumeAckMessage {
  type: "resumeAck";
  received: number;
  /** Server boot-time nanoseconds since unix epoch. Optional for
   *  back-compat with pre-CONN-01 server builds (which omit it). */
  serverEpoch?: number;
}

export interface ModesMessage {
  type: "modes";
  bracketedPaste: boolean;
  applicationCursor: boolean;
  applicationKeypad: boolean;
  mouseSGR: boolean;
  focusReporting: boolean;
  reverseVideo: boolean;
  mouseMode: number; // 0=off, 1000=normal, 1002=button-event, 1003=any-event
  inputAck?: number;
}

export interface TitleMessage {
  type: "title";
  title: string;
  inputAck?: number;
}

export type ServerMessage =
  | ScreenMessage
  | ScrollMessage
  | ResumeAckMessage
  | ModesMessage
  | TitleMessage;

export type ControlMessage =
  | { type: "resize"; cols: number; rows: number }
  | { type: "resume"; sessionId: string; sentBytes: number; scrollbackHave: number };
