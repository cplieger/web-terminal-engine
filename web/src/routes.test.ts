// The route constants are a CROSS-LANGUAGE CONTRACT: the Go terminal package
// exports the same paths (WSPath, SessionsPath, SessionEventsPath) and mounts
// exactly this topology via MountSessionRoutes. These tests pin the TS half to
// the literal wire values so a rename on either side fails a build instead of
// 404ing the shipped UI at runtime.
import { describe, it, expect } from "vitest";
import { WS_PATH, SESSIONS_PATH, SESSION_EVENTS_PATH } from "./routes.js";
import { wsURL } from "./wsurl.js";

describe("session-route contract (TS half of the Go terminal consts)", () => {
  it("carries the exact wire paths the Go MountSessionRoutes mounts", () => {
    expect(WS_PATH).toBe("/ws");
    expect(SESSIONS_PATH).toBe("/api/sessions");
    expect(SESSION_EVENTS_PATH).toBe("/api/sessions/events");
  });

  it("events path sits under the sessions subtree (ServeMux specificity contract)", () => {
    expect(SESSION_EVENTS_PATH.startsWith(SESSIONS_PATH + "/")).toBe(true);
  });

  it("wsURL defaults to WS_PATH", () => {
    expect(wsURL("https:", "example.test")).toBe("wss://example.test" + WS_PATH);
  });
});
