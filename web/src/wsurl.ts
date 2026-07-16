import { WS_PATH } from "./routes.js";

// Pure URL helper, no DOM / terminal / WebSocket dependencies.
// Kept in a dedicated module so it can be property-tested without
// touching `location`. The caller threads the live `location.*` through.

/**
 * Build the WebSocket URL for a terminal session from a page
 * protocol/host pair and a path. `https:` selects `wss:`; any other
 * page protocol selects `ws:`.
 *
 * @param pageProtocol `location.protocol` (e.g. `"https:"`).
 * @param pageHost     `location.host` (host[:port]).
 * @param path         WebSocket endpoint path; defaults to `WS_PATH` (`"/ws"`).
 * @returns            A parseable `ws://`/`wss://` URL.
 */
export function wsURL(pageProtocol: string, pageHost: string, path = WS_PATH): string {
  const wsProto = pageProtocol === "https:" ? "wss:" : "ws:";
  const normalizedPath = path.startsWith("/") ? path : `/${path}`;
  return `${wsProto}//${pageHost}${normalizedPath}`;
}
