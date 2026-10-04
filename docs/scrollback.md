# Scrollback and reconnect

This page covers how far back a session's history reaches, how a browser fetches old history on demand, and how a page can restore its scrollback after a reload. It is for a developer who sets the history depth or wires the TypeScript client by hand.

## Retained history

Each session keeps the lines that scroll off its screen in an in-memory ring, addressed by absolute line index. That depth is how far back a user can scroll, and it is what a reconnect can replay.

`WithScrollbackCapacity(n)` sets the depth. The default is `terminal.DefaultScrollbackCapacity`, 100000 lines. `0` turns retention off. The live screen still works, and nothing survives scrolling off it.

The memory cost depends on what the session printed. Measured on this ring, a full 100000 lines is about 7 MB of short lines, 21 MB of ordinary 80-column lines and 64 MB of dense 200-column styled output. The buffer grows as history arrives rather than being allocated at the ceiling, so a large capacity costs a short-lived session nothing. For the same reason there is no "unlimited" value. A number larger than any session will reach means unlimited.

`(*Handler).ScrollbackBounds()` returns the newest committed index and the oldest index a resume can still ask for.

## Choosing a depth

| Symbol | Purpose |
| --- | --- |
| `ScrollbackEnvVar` | `SCROLLBACK`, the variable name to read. The engine does not read it. Your server reads it and passes the option. |
| `DefaultScrollbackCapacity` | The default, so your server can log the depth in effect without hard-coding it. |
| `MinPagingCapacity` | The depth at or above which the handler offers demand-paged scrollback to the client. |
| `ClampScrollbackCapacity(n)` | Turns an operator's number into the one to configure, plus a reason when it changed it. |

`ClampScrollbackCapacity` exists because one range does the opposite of what the operator wants. A depth between 1 and `MinPagingCapacity` is kept by the ring but is too shallow for the server to offer paged history. A client that can page then holds its whole older buffer in memory instead. Lowering the server number to save memory would spend more of it in the browser, so the function raises that range to `MinPagingCapacity` and says why. A negative number becomes 0. `0` passes through, because a client cannot page against a server that holds no history.

## Demand-paged scrollback

With paging, a deep server history stays reachable without the browser holding all of it. The server announces the capability when the client resumes, limits how much each reconnect replays, and answers `history` requests for the ranges the client asks for. The client keeps a resident tail of the newest lines, plus a cache of the pages a reader actually visited.

`createTerminalEngine` wires paging for you. If you compose the parts by hand, wire all of it, because a half-wired setup fails without an error:

- Pass `requestHistory` and `historyBudget`, taken from the connection, to `createRenderer`.
- Pass `onScrollPosition: () => renderer.handleScrollPosition()` to `createScrollController`. It is the only signal that triggers a fetch, because `onUserScrollChange` fires only when the view switches between following and holding.
- Pass the renderer to `createConnection`, which reads its resume and history answers from it.

Your code owns the cache's inactivity timeout, because the engine has no idea of a page or a tab. The renderer exposes `browseCacheSize()`, `lastBrowseActivityMs()` and `dropBrowseCache()` for it. [web-terminal-ui](https://github.com/cplieger/web-terminal-ui) wires all of this.

## How a reconnect resumes

On reconnect, the client sends its session id, the highest line index it already holds and the number of input bytes it has sent. The server's reply carries its epoch, which identifies one server process.

- When the epoch differs from the one the client last saw, the server is a new process, and the client resets and repaints.
- Otherwise the server sends every retained line after that index, then the current screen, cursor and modes.
- When the server has already dropped some of the lines the client needs, it says so, and the client shows that earlier output was trimmed.

## Restoring scrollback after a reload

A browser can discard a background page. `LineStore` lets the page resume with only the missing lines instead of refilling its whole buffer over the network.

- `LineStore.snapshot(serverEpoch, maxLines?)` returns the newest retained lines as plain data that `structuredClone` can copy. It returns `null` for an empty store, so a caller cannot overwrite a good snapshot with an empty one.
- `LineStore.fromSnapshot(snap, maxLines?)` rebuilds a store. It returns `null` instead of throwing, and it never restores half a snapshot. Every failure has the same correct handling, which is to start empty and take a full resume.

Storage is yours. The engine only supplies the data.

The `serverEpoch` argument is required, and it is the part to get right. Line indices only mean something within one server process. A restored store that is not checked against the live server shows old content as live, and then refuses the new session's low-index output as stale. Read the epoch with `connection.serverEpochOf(sessionId)` when you save. Seed it back with `connection.adoptPersistedEpoch(sessionId, epoch)` before you connect, which sends a mismatch through the usual `onServerRestart` path.

`connection.currentSessionId()` returns the id of an unmanaged single terminal. That id is stored per browser tab in `sessionStorage`, and it is the key to save the snapshot under.
