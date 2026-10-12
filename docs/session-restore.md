# Restoring sessions

This page covers how a Go server built on the `terminal` package brings its sessions back after a restart. It is for a developer whose host should reopen the same tabs, in the same order and layout, when the process starts again.

## What the host owns

Sessions end when the server stops. A host that wants them back after a restart saves the session set and restores it at the next start. The engine gives you the snapshot and the restore. You choose where the bytes live and how often they are written.

## Capturing the set

`(*SessionManager).CaptureSessionSet(resume)` returns a `SessionSet`. It holds the display order, each session's alias and pinned title, and the pane layout by position. It holds no session id, because a session id lets anyone who has it attach to that terminal.

The `resume` callback returns a token per session, for example the conversation id of the program running in it. The engine stores the token and never reads it. The callback runs outside the manager lock, so it may call `List`. The sessions, their order and the layout come from one moment, so a tab opened during the capture is either fully in the set or fully out of it. A session that exited normally is left out, and a crashed one is kept. Only the first 512 sessions in the display order are kept. A token over 1024 bytes or not valid UTF-8 is dropped. When the set would still encode to more than 1 MiB, tokens are dropped from the last session backwards until it fits.

## Reading it back

Marshal the set with `encoding/json` and store it. Write it atomically with owner-only permissions: write a temporary file, sync it, rename it over the old file, then sync the directory. A crash then leaves the old set or the new one, never a partial file. Read it back with a bounded read, refusing a file over `MaxSessionSetBytes` before reading it whole. `ParseSessionSet(data)` reads it back and refuses the whole set, wrapping `ErrInvalidSessionSet`, when any of these rules fails:

- The data is at most 1 MiB, is valid UTF-8, and is one JSON object with nothing after it. Unknown fields are ignored.
- `version` is 1.
- There are at most 512 sessions.
- Each alias is empty or valid, and no two are equal.
- Each pinned title is already clean, as `SetSessionPinnedTitle` would store it.
- Each resume token is at most 1024 bytes.
- The layout's indices are in range and distinct, and its shape follows the [pane layout rules](sessions.md#pane-layout).

## Restoring

`(*SessionManager).RestoreSessionSet(ctx, set, plan)` creates the sessions in order. For each entry, `plan` returns the `CreateSpec` to use, or `false` to skip it. A host uses `plan` to turn its token back into a command, for example a program's resume flag.

The restore then puts the restored sessions first in the order and shows the stored layout over them. A client that set its own order or layout while the restore ran keeps it. One create that fails is logged and counted in `Failed`, and the rest continue. A cancelled `ctx` or a `Shutdown` stops the restore early, and the sessions already restored are still arranged. A second call while one runs returns `ErrRestoreRunning`.

While the restore runs, `POST /api/sessions` and `GET /api/sessions/layout` answer 503, as [Pane layout](sessions.md#pane-layout) describes, so a page that waits for the layout read lists the whole restored set. A host that serves requests before it calls `RestoreSessionSet` holds both routes itself until the call starts. A middleware passed through `WithCreateGate` can do it, answering 503 `RestoringMessage` with `Retry-After: 1`. Otherwise a layout read in that window answers 200, and the page opens a new session beside the restored ones.

`ExampleSessionManager_CaptureSessionSet` in the package documentation shows the whole loop around a host's own load and save functions.
