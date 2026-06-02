// Binary wire decoder. Mirrors wire_binary.go on the server.
//
// All multi-byte integers are little-endian. See the Go terminal package's
// wire_binary.go for the exact frame layout.

import type {
  WireRun,
  ScreenMessage,
  ScrollMessage,
  ResumeAckMessage,
  ModesMessage,
  TitleMessage,
  ServerMessage,
} from "./types.js";

// Wire message type tags (mirrored from Go wire_binary.go constants).
const MSG_SCREEN = 0;
const MSG_SCROLL = 1;
const MSG_RESUME_ACK = 2;
const MSG_MODES = 3;
const MSG_TITLE = 4;
const MODE_FLAG_BRACKETED_PASTE = 1;
const MODE_FLAG_APP_CURSOR_KEYS = 2;
const MODE_FLAG_MOUSE_SGR = 4;
const MODE_FLAG_FOCUS_REPORTING = 8;
const MODE_FLAG_APP_KEYPAD = 16;
const MODE_FLAG_REVERSE_VIDEO = 32;

class Cursor {
  view: DataView;
  bytes: Uint8Array;
  off = 0;
  constructor(buf: ArrayBuffer) {
    this.view = new DataView(buf);
    this.bytes = new Uint8Array(buf);
  }
  u8(): number {
    const v = this.view.getUint8(this.off);
    this.off += 1;
    return v;
  }
  u16(): number {
    const v = this.view.getUint16(this.off, true);
    this.off += 2;
    return v;
  }
  i32(): number {
    const v = this.view.getInt32(this.off, true);
    this.off += 4;
    return v;
  }
  u64(): number {
    // Number(v) is safe here: all values this protocol encodes in u64
    // fields (bytesReceived, serverEpoch) fit within JavaScript's 53-bit
    // integer precision. If the protocol ever needs true 64-bit values,
    // this must return BigInt instead.
    const v = this.view.getBigUint64(this.off, true);
    this.off += 8;
    return Number(v);
  }
  utf8(len: number): string {
    const slice = this.bytes.subarray(this.off, this.off + len);
    this.off += len;
    return new TextDecoder().decode(slice);
  }
}

function readRowRuns(c: Cursor): WireRun[] {
  const numRuns = c.u16();
  // eslint-disable-next-line @typescript-eslint/no-unsafe-assignment -- pre-allocated array filled immediately below
  const runs: WireRun[] = new Array(numRuns);
  for (let i = 0; i < numRuns; i++) {
    const tlen = c.u16();
    const t = c.utf8(tlen);
    const f = c.i32();
    const b = c.i32();
    const a = c.u16();
    const uc = c.i32();
    const ulen = c.u16();
    const u = ulen > 0 ? c.utf8(ulen) : undefined;
    runs[i] = u ? { t, f, b, a, uc, u } : { t, f, b, a, uc };
  }
  return runs;
}

export function decodeWireBinary(buf: ArrayBuffer): ServerMessage | null {
  if (buf.byteLength < 9) {
    return null;
  }
  try {
    return decodeWireBinaryInner(buf);
  } catch (err) {
    // Graceful frame drop: a RangeError means the frame was truncated
    // or malformed (e.g. network split mid-frame). Returning null lets
    // the caller skip this frame rather than crashing the decode loop.
    // The next flush will carry a complete snapshot anyway.
    if (err instanceof RangeError) {
      return null;
    }
    throw err;
  }
}

function decodeWireBinaryInner(buf: ArrayBuffer): ServerMessage | null {
  const c = new Cursor(buf);
  const msgType = c.u8();
  const inputAck = c.u64();

  if (msgType === MSG_RESUME_ACK) {
    // Optional 8-byte server epoch tail: present when the server includes
    // its boot timestamp so the client can detect restarts. Older servers
    // may omit it, so we check byteLength before reading. On mismatch
    // the client resets its resume state rather than silently losing input.
    let serverEpoch: number | undefined;
    if (buf.byteLength >= 17) {
      serverEpoch = c.u64();
    }
    const msg: ResumeAckMessage =
      serverEpoch !== undefined
        ? { type: "resumeAck", received: inputAck, serverEpoch }
        : { type: "resumeAck", received: inputAck };
    return msg;
  }
  if (msgType === MSG_SCREEN) {
    // Sparse row array: the frame carries only changed rows (indexed by
    // row_idx). The client allocates a full screenHeight-sized array but
    // only the indices listed in `changed` are populated. Unchanged rows
    // retain their previous DOM state — this keeps frame size O(changed).
    const cursorRow = c.u16();
    const cursorCol = c.u16();
    const screenHeight = c.u16();
    const numChanged = c.u16();
    const cursorStyle = c.u8();
    const cursorFlags = c.u8();
    const cursorHidden = (cursorFlags & 1) !== 0;
    const bell = (cursorFlags & 2) !== 0;
    const cursorBlink = (cursorFlags & 4) !== 0;
    // eslint-disable-next-line @typescript-eslint/no-unsafe-assignment -- pre-allocated array filled below
    const rows: WireRun[][] = new Array(screenHeight);
    // eslint-disable-next-line @typescript-eslint/no-unsafe-assignment -- pre-allocated array filled below
    const changed: number[] = new Array(numChanged);
    for (let i = 0; i < numChanged; i++) {
      const idx = c.u16();
      changed[i] = idx;
      rows[idx] = readRowRuns(c);
    }
    const msg: ScreenMessage = {
      type: "screen",
      rows,
      cursor: [cursorRow, cursorCol],
      changed,
      cursorStyle,
      cursorHidden,
      cursorBlink,
      bell,
      inputAck,
    };
    return msg;
  }
  if (msgType === MSG_SCROLL) {
    const numLines = c.u16();
    // eslint-disable-next-line @typescript-eslint/no-unsafe-assignment -- pre-allocated array filled below
    const lines: WireRun[][] = new Array(numLines);
    for (let i = 0; i < numLines; i++) {
      lines[i] = readRowRuns(c);
    }
    const msg: ScrollMessage = { type: "scroll", lines, inputAck };
    return msg;
  }
  if (msgType === MSG_MODES) {
    const flags = c.u8();
    const mouseMode = c.u16();
    const msg: ModesMessage = {
      type: "modes",
      bracketedPaste: (flags & MODE_FLAG_BRACKETED_PASTE) !== 0,
      applicationCursor: (flags & MODE_FLAG_APP_CURSOR_KEYS) !== 0,
      applicationKeypad: (flags & MODE_FLAG_APP_KEYPAD) !== 0,
      mouseSGR: (flags & MODE_FLAG_MOUSE_SGR) !== 0,
      focusReporting: (flags & MODE_FLAG_FOCUS_REPORTING) !== 0,
      reverseVideo: (flags & MODE_FLAG_REVERSE_VIDEO) !== 0,
      mouseMode,
      inputAck,
    };
    return msg;
  }
  if (msgType === MSG_TITLE) {
    const titleLen = c.u16();
    const title = c.utf8(titleLen);
    const msg: TitleMessage = { type: "title", title, inputAck };
    return msg;
  }
  return null;
}
