// Public wire-compatibility metadata for the independently released browser
// client. The Go server exposes the complementary directional floor from its
// terminal package. Keep both current revisions equal; their minimum peer
// revisions may diverge as each receiver retires old decode paths.

/** Current wire revision emitted by this browser client. */
export const WIRE_PROTOCOL_VERSION = 4;

/** Oldest explicitly declared server revision this client accepts. */
export const MIN_SUPPORTED_SERVER_WIRE_VERSION = 3;

/** Definitive private WebSocket close code for an incompatible declared peer. */
export const WIRE_INCOMPATIBLE_CLOSE_CODE = 4002;

/** Shape of the machine-readable compatibility metadata object. */
export interface WireCompatibility {
  readonly protocolVersion: number;
  readonly minimumServerProtocolVersion: number;
  readonly incompatibleCloseCode: number;
}

/** Machine-readable compatibility metadata shipped in every TS release. */
export const WIRE_COMPATIBILITY: WireCompatibility = Object.freeze({
  protocolVersion: WIRE_PROTOCOL_VERSION,
  minimumServerProtocolVersion: MIN_SUPPORTED_SERVER_WIRE_VERSION,
  incompatibleCloseCode: WIRE_INCOMPATIBLE_CLOSE_CODE,
});

/** Details surfaced when the connection is stopped for wire incompatibility. */
export interface WireIncompatibility {
  readonly source: "server-version" | "server-close";
  readonly clientVersion: number;
  readonly minimumServerVersion: number;
  readonly serverVersion?: number;
  readonly reason: string;
}
