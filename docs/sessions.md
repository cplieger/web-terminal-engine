# Sessions and processes

This page covers the server side of the Go `terminal` package. It describes one session's handler, the session manager that runs many, the routes it serves, and what happens to the processes a session starts. It is for a developer wiring the engine into a Go server.

## One session

`NewHandler(command, ...Option)` returns a `Handler` that runs `command` in a PTY and serves one WebSocket. Mount it with `RegisterRoutes(mux)`, which serves `/ws`, or use it as an `http.Handler` directly. The handler owns the PTY lifecycle, the binary wire protocol, reconnect with scrollback replay, and an adaptive ping.

The options are `WithWorkDir`, `WithLogger`, `WithEnv`, `WithScrollbackCapacity`, `WithOriginPolicy`, `WithOnProcessExit`, `WithKeepUnfocused`, `WithTheme`, `WithMinimumContrast`, `WithCommandLogValue`, `WithContainment` and `WithContainmentSampleInterval`.

A handler stops in one of two ways:

- `Close()` ends the session and returns at once. The cgroup teardown, the `/proc` sweep and the client notification continue in the background. Use it from a request handler or a timer.
- `Shutdown(ctx)` does the same and then waits for all of it. It returns `ctx.Err()` when the budget runs out first. Use it when the process is about to exit, because a process that exits during an unfinished teardown loses it.

`(*Handler).ScrollbackBounds()` returns the session's retained-history bounds as one atomic pair. The first value is the committed index, one past the newest committed line. The second is the oldest index a resume can still ask for. Both follow the child's output and the configured capacity, so the call is read-only.

`(*Handler).ExitError()` returns the `cmd.Wait()` error the session kept, which is what decides between the `exited` and `crashed` statuses below.

## Many sessions

`NewSessionManager(factory, ...ManagerOption)` fronts any number of PTY sessions. The factory builds one `Handler` per session id. The manager serves three handlers:

- `WebSocketHandler()` attaches a browser to a session at `/ws?session=<id>`.
- `RESTHandler()` serves `/api/sessions` and the per-session routes below it.
- `EventsHandler()` serves the status stream at `/api/sessions/events` as server-sent events.

`MountSessionRoutes(mux, SessionHandlers{WS, REST, Events}, ...MountOption)` mounts exactly the route constants `WSPath`, `SessionsPath`, `SessionsSubtreePath` and `SessionEventsPath`. The REST handler needs both `SessionsPath` and `SessionsSubtreePath`. These paths match the defaults the TypeScript client exports as `WS_PATH`, `SESSIONS_PATH` and `SESSION_EVENTS_PATH`. `(*SessionManager).MountAPI(mux, opts...)` does the same for one manager.

Each `POST /api/sessions` starts a process. `WithCreateGate(mw)` wraps the whole REST handler in your own middleware, for example a rate limit that picks out session creation.

The manager options are `WithManagerLogger`, `WithManagerOriginPolicy`, `WithIdleReaper`, `WithStatusClassifier` and `WithSessionActivity`.

`(*SessionManager).Shutdown(ctx)` signals every session before it waits on any of them, so their teardown windows overlap. When the budget runs out, it reports how many sessions were still unfinished. A manager is single-use. `Shutdown` stops the status sweep and the idle reaper and starts neither again.

## The REST routes

| Route | What it does |
| --- | --- |
| `GET /api/sessions` | Lists the live sessions. |
| `POST /api/sessions` | Creates a session and starts its process. |
| `DELETE /api/sessions/{id}` | Closes a session and ends its process. |
| `PUT /api/sessions/order` | Sets the display order every viewer shares. |
| `GET` and `PUT /api/sessions/layout` | Reads and sets the pane layout every viewer shares. |
| `PUT /api/sessions/{id}/title` | Stores a title the client derived, such as the first line the user submitted. |
| `PUT` and `DELETE /api/sessions/{id}/pinned-title` | Sets and clears a name the user chose. |

### Display order

`PUT /api/sessions/order` takes every live session id in the wanted order. The server refuses it with 409 unless the list names the live sessions exactly once each. That rule makes the write atomic and shows a caller that its view is stale. Each session's position comes back as `order` on the list and on the status stream, so a reorder made in one browser moves the tab in every other one.

Both the list and the status stream serve sessions in that order, then by oldest `createdAt`, then by session id. A client that builds a tab strip from whichever one arrives first gets the same strip every time.

### Pane layout

The layout record says which session each of two panes shows and which pane receives typing. Its fields are:

- `left` and `right`, each a session id or `null` for an empty pane.
- `open`, whether the split is shown.
- `handle`, the left pane's share of the row, from 0 to 1. It is fixed at 0.5 while the split is closed.
- `selected`, `"left"` or `"right"`, the pane that receives typing. Its session is the active tab.

A `PUT` must carry `open`, `handle` and `selected`. The server answers 400 with the text of the first rule the record breaks. A closed split has no right pane and selects the left. One session cannot show on both sides. A selection cannot rest on an empty pane while the other pane, or any live session, has something to show. The server answers 409 when a side names a session that is not live, and a client handles it as it handles the order's 409.

The server keeps the record valid on its own. The first session of an empty layout shows on the left. A closing session leaves its pane, the selection moves to a pane that still shows something, and an empty record is refilled from the order. The idle reaper and `Shutdown` reset the record.

A client reads the layout once at load, and the status stream does not carry layout changes. When one viewer writes a new layout, another open viewer keeps its old layout until it reloads. A reload restores whatever the last writer stored, as it does for the order.

## Status, activity and titles

Each session carries a status the server derives:

| Status | Meaning |
| --- | --- |
| `working` | The program reports progress with `OSC 9;4` state 1 or 3. |
| `failed` | The program reports `OSC 9;4` state 2, an error. |
| `warning` | The program reports `OSC 9;4` state 4. |
| `idle` | The program reports no progress state, and no notification is latched. |
| `input` | A notification said the program waits for the user. |
| `done` | A notification said the turn finished. |
| `exited` | The process ended normally, or the server ended it. |
| `crashed` | The process ended with a non-zero status or a signal. |

`OSC 9;4` progress is read with iTerm2's state meanings. A session the server closed itself, through `Close`, the idle reaper or a shutdown, ends as `exited`. That way a routine restart never reports a failure.

`input` and `done` come from an OSC 9 notification passed through the classifier you install with `WithStatusClassifier`. Such a latch holds until a later progress state contradicts it. The active states 1 and 3 and the error state 2 contradict it. The paused state 4 does not, because "stopped, resumable by the user" agrees with a needs-input latch.

Some programs notify when a wait starts and stay silent when it ends. A consumer that sees the end from its own sources reads the latch with `(*SessionManager).StatusLatch(id)`. It returns the latched status, the notification sequence that set it, and whether the session is tracked. `WithdrawStatusLatch(id, want, seq)` clears it as a compare-and-swap. It refuses an unknown session, a `want` other than `input` or `done`, a latch that no longer equals `want`, and a latch a newer notification set after `seq`. A slow poller can therefore never erase a fresh request. The next sweep recomputes the status from progress alone.

Each status event also carries:

- `progressValue`, the `OSC 9;4` percentage, or -1 when the program reported none. -1 is not 0%.
- `notification` and `notificationSeq` for a fresh OSC 9 message, so a consumer with no classifier still receives it.

### Background activity

`WithSessionActivity(fn)` reports a second value beside the status, for a background task that outlives the turn that started it. `activity` is `""` for none, `working` while a background task runs, `waiting` when it is stopped and resumable with nobody being asked, and `input` when it waits for the user. `activityCount` is the number of sources in that state, 1 or more whenever `activity` is set. Both fields are always present.

The engine calls `fn` once per session on each status sweep, on each session list, and for each new status-stream subscriber. These calls come from different goroutines, so `fn` must be safe for concurrent use, must not block and must not do I/O. Activity never enters the status, never touches the latch, and never sets `reportsActivity`, so a background task does not light the tab's turn indicator. Without the option, every session's activity is empty.

### Titles

The server resolves each session's `title` from four sources, in this order:

1. The name the user pinned.
2. The window title the program set with OSC 0 or OSC 2.
3. A title the client asked the server to remember with `PUT /api/sessions/{id}/title`.
4. The foreground process or the working directory.

Every attached client shows the same label without computing it again. `pinnedTitle` travels beside it, so a client can tell a chosen name from an inferred one.

### Several clients on one session

When several clients share a session, a live resize goes to the last writer. When a client disconnects, the screen relaxes to the smallest size among the clients that remain.

## The processes a session starts

A session's process inherits the server's environment plus a default terminal identity: `TERM=xterm-256color`, `COLORTERM=truecolor`, `TERM_PROGRAM=iTerm.app` and `TERM_PROGRAM_VERSION=3.6.6`. Programs then detect truecolor, `OSC 9;4` progress reporting and DEC 2026 synchronized output. `WithEnv` values come after these defaults, so your entry for the same variable wins.

### Session reaping

On Linux, a closed session does not leave processes behind. A child that calls `setsid()` leaves its process group and its session, so neither `kill(-pgid)` nor the `SIGHUP` from closing the PTY reaches it. A process moved to init has no ancestry left to follow. Some agent runtimes do exactly this, and some never exit on end of input.

The engine spawns every session with one unguessable environment variable, `WT_SESSION_REAP`. `execve` copies it into every descendant, and it survives both `setsid()` and the move to init. At session end the engine finds every process that still carries it and stops them in four steps. It waits, sends `SIGTERM`, waits again, then sends `SIGKILL`. It logs one `session reap reclaimed escaped processes` line with `survivors`, `term_reclaimed`, `kill_forced` and `resident_bytes`, and only when it had to reclaim something.

This needs no capability, no cgroup, no mount and no PID namespace. In an unprivileged container, reclaiming a `setsid()` escapee took 354 ms, and a full scan of 17,547 processes took about 81 ms. Reaping is always on, with two limits:

- A descendant that `execve`s with a deliberately emptied environment escapes it.
- A tree that forks during teardown can outrun one scan. Each step scans again rather than reusing the first list.

The engine's value is the only assignment to `WT_SESSION_REAP` in the child environment. It removes that key from your `WithEnv` and from the server's inherited environment before it adds its own. Setting the variable yourself therefore does nothing, and a server started inside one of these sessions does not pass its parent's value on.

`Containment` (`NewContainment`, `WithContainment`) is the stronger boundary, and it is opt-in. It puts each session in its own cgroup, which nothing can escape, and it reports per-session `memory.peak` and `pids.peak`.

### Zombie processes

`StartZombieReaper(log, interval)` solves a separate problem and is also opt-in. Session reaping stops processes that are still alive. The zombie reaper collects the exit status of finished processes nobody waited for.

A server running as its container's PID 1 inherits every orphan in the container. Go's `os/exec` waits only on the children it created, so every language server and every `git` a session started stays behind as a zombie on the server. One container measured 17,323 zombies.

Call `StartZombieReaper` from the composition root of a server that is, or may become, PID 1. It sets `PR_SET_CHILD_SUBREAPER`, so orphans arrive even behind an init shim. It then sweeps every `interval`, 30 seconds when you pass 0 and never less than 1 second, and returns a stop function.

It is a periodic sweep rather than a `SIGCHLD` handler, because `signal.Notify` is process-wide state and the Go runtime uses `SIGCHLD` for `os/exec`. It never waits on a process the engine spawned. A lock held across each spawn guards the list of those processes, so a generic `wait(-1)` cannot take a session's exit status.

### Platforms

The PTY needs a Unix-like system such as Linux or macOS. Session reaping and the zombie reaper do nothing outside Linux. `NewContainment` returns an error there, and the process-based title falls back to the command's name.
