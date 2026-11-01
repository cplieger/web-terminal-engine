# Sessions and processes

This page covers the server side of the Go `terminal` package. It describes one session's handler, the session manager that runs many, the routes it serves, and what happens to the processes a session starts. It is for a developer wiring the engine into a Go server.

## One session

`NewHandler(command, ...Option)` returns a `Handler` that runs `command` in a PTY and serves one WebSocket. Mount it with `RegisterRoutes(mux)`, which serves `/ws`, or use it as an `http.Handler` directly. The handler owns the PTY lifecycle, the binary wire protocol, reconnect with scrollback replay, and an adaptive ping.

The options are `WithWorkDir`, `WithLogger`, `WithEnv`, `WithScrollbackCapacity`, `WithOriginPolicy`, `WithOnProcessExit`, `WithKeepUnfocused`, `WithTheme`, `WithMinimumContrast`, `WithCommandLogValue`, `WithContainment` and `WithContainmentSampleInterval`.

A handler stops in one of two ways:

- `Close()` ends the session and returns at once. The cgroup teardown, the `/proc` sweep and the client notification continue in the background. Use it from a request handler or a timer.
- `Shutdown(ctx)` does the same and then waits for all of it. It returns `ctx.Err()` when the budget runs out first. Use it when the process is about to exit, because a process that exits during an unfinished teardown loses it.

`(*Handler).ScrollbackBounds()` returns the session's retained-history bounds as one atomic pair. The first value is the committed index, one past the newest committed line. The second is the oldest index a resume can still ask for. Both follow the child's output and the configured capacity, so the call is read-only.

`(*Handler).Text(maxLines)` returns the newest `maxLines` lines of the session as plain text, read from the parsed screen rather than the byte stream. It covers retained history and the main screen. A line the terminal wrapped comes back as one line, trailing blanks are dropped, and no escape sequences remain. While a full-screen program such as vim holds the alternate screen, it returns the main screen beneath it and sets `AltScreen`. A value of `0` or less returns all retained history. The second result is `false` until the process has started.

`(*Handler).ExitError()` returns the `cmd.Wait()` error the session kept, which is what decides between the `exited` and `crashed` statuses below.

## Many sessions

`NewSessionManager(factory, ...ManagerOption)` fronts any number of PTY sessions. The factory builds one `Handler` per session id. The manager serves three handlers:

- `WebSocketHandler()` attaches a browser to a session at `/ws?session=<id>`.
- `RESTHandler()` serves `/api/sessions` and the per-session routes below it.
- `EventsHandler()` serves the status stream at `/api/sessions/events` as server-sent events.

`MountSessionRoutes(mux, SessionHandlers{WS, REST, Events}, ...MountOption)` mounts exactly the route constants `WSPath`, `SessionsPath`, `SessionsSubtreePath` and `SessionEventsPath`. The REST handler needs both `SessionsPath` and `SessionsSubtreePath`. These paths match the defaults the TypeScript client exports as `WS_PATH`, `SESSIONS_PATH` and `SESSION_EVENTS_PATH`. `(*SessionManager).MountAPI(mux, opts...)` does the same for one manager.

Each `POST /api/sessions` starts a process. `WithCreateGate(mw)` wraps the whole REST handler in your own middleware, for example a rate limit that picks out session creation.

`(*SessionManager).Create()` starts a session from the factory. `(*SessionManager).CreateWith(spec)` does the same with a `CreateSpec`: `Build` replaces the factory for that one session, `Alias` names it when the alias is valid and free, and `PinnedTitle` pins its title. The alias and the title are in place before any client can list the session. After `Shutdown`, both `Create` and `CreateWith` return `ErrManagerClosed` and start nothing, and `POST /api/sessions` answers 503.

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
| `PUT /api/sessions/{id}/seen` | Marks the session's status as read for every reader, with the body `{"seq": N}`. |

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

While `RestoreSessionSet` runs, `POST /api/sessions` and `GET /api/sessions/layout` answer 503 with `Retry-After: 1` and the body `{"error":"restoring tabs"}`. The list, the status stream and every `PUT` route keep working. A client that waits for the layout read to succeed and lists afterwards sees the whole restored set.

## Saving and restoring the session set

Sessions end when the server stops. [Restoring sessions](session-restore.md) covers how a host saves the session set and brings it back after a restart.

## Status, activity and titles

Each session carries a status the server derives, and exactly one row of the table applies to a session at a time. "The record the status comes from" is the `OSC 7501` record [Program status](#program-status-osc-7501) folds the status from, which exists only once the program has sent a report since the last hard reset (RIS). A notification is latched as [Notifications](#notifications) describes. `TestStatusTableRowsAreExclusive` in `terminal/status_program_test.go` checks each row's condition against the server's derivation and that no two rows hold at once.

| Status | Condition |
| --- | --- |
| `exited` | The process ended normally, or the server ended it. |
| `crashed` | The process ended with a non-zero status or a signal, and the server did not end it. |
| The latched status | The process runs and a notification is latched: the status is whatever the classifier you install with `WithStatusClassifier` returned for it, such as `input` or `done`. |
| `working` | The process runs, no notification is latched, and the record the status comes from is `working`, or the program has sent no `OSC 7501` report since the last hard reset (RIS) and its `OSC 9;4` state is 1 or 3. |
| `failed` | The process runs, no notification is latched, and the record the status comes from is `error`, or the program has sent no `OSC 7501` report since the last hard reset (RIS) and its `OSC 9;4` state is 2. |
| `warning` | The process runs, no notification is latched, the program has sent no `OSC 7501` report since the last hard reset (RIS), and its `OSC 9;4` state is 4. |
| `input` | The process runs, no notification is latched, and the record the status comes from is `blocked`. |
| `done` | The process runs, no notification is latched, and the record the status comes from is `done`. |
| `idle` | No other row applies and no notification is latched: the program's `OSC 7501` records leave no record the status comes from, or the record the status comes from is `idle`, or it has sent no `OSC 7501` report since the last hard reset (RIS) and its `OSC 9;4` progress is cleared (state 0) or was never sent. |

`OSC 9;4` progress is read with iTerm2's state meanings. A session the server closed itself, through `Close`, the idle reaper or a shutdown, ends as `exited`. That way a routine restart never reports a failure.

### Program status (OSC 7501)

The engine implements the [Program Status Protocol](https://www.superlogical.com/rex/docs/build/program-status), revision 0.4. Once a program sends an `OSC 7501` report, that report decides the status and `OSC 9;4` no longer does, as the protocol asks. The engine keeps drawing the `OSC 9;4` percentage in `progressValue`, though, unless the report carries its own `progress`. A hard reset (RIS) forgets every report and hands the status back to `OSC 9;4`. `reportsActivity` stays true.

The engine keeps up to 64 records per session, one per `id`. The status comes from the root record, the one with no `id`. A `blocked` record wins over it, because a worker waiting for the user is what the status exists to show: a `blocked` root first, otherwise the most recently reported `blocked` child. With no root record and nothing blocked, the status is `idle`. Which reports make a new occurrence of the status is stated once, under `statusSeq` in [Status event fields](#status-event-fields).

A report with no state or an unknown one is ignored whole, and so is one in which any pair breaks one of the protocol's limits or has a `msg` or `title` that is not base64 or decodes to a control character. Each pair meets the first of these rules that applies: a key over 16 bytes discards the report, even with no `=`; any other pair with no `=` is skipped; a value over its key's limit discards the report; an `id` pair, even one with a byte outside the protocol's value set, becomes the report's `id`, and the report is ignored unless its last `id` pair holds a well-formed id, so a malformed `id` never sends it to the root record; any other value outside the value set is skipped; a `msg` or `title` that is not base64 of text without control characters, within its decoded size, discards the report; an unknown key is skipped. A skipped pair leaves the value an earlier pair gave that key, a repeated key keeps its last value, and an unrecognised `kind`, `progress` or `app` counts as absent. The engine validates `msg` and `title` but does not store them. A program that asks with `OSC 7501 ; ?` gets the reply `OSC 7501 ; ? : states=idle,working,done,blocked,error : kinds=permission,question,auth`. Nothing from a report is ever written back.

#### Record lifecycle

Each row is an event and each column the state of the record it reaches. A cell gives the record's next state, and "new" marks a new occurrence of the status it shows: `statusSeq` rises, so the status reads as unread again. A record the status does not show, such as a child that is not `blocked`, makes no new occurrence. The last column is `reportsActivity`, which never falls back to false. The section of revision 0.4 each event comes from is in brackets.

| Event | absent | `working` | `blocked` | `done` | `error` | `idle` | `reportsActivity` |
| --- | --- | --- | --- | --- | --- | --- | --- |
| A report for this record's `id` [Records and ids] | the reported state, new unless it is `idle` | the reported state, new unless it is `working` again | the reported state, new unless it is `blocked` with the same `kind` again | the reported state, new unless it is `done` again | the reported state, new unless it is `error` again | the reported state, new unless it is `idle` again | true |
| `clear` for this `id`, an ancestor of it, or the root [States] | absent | absent, new | absent, new | absent, new | absent, new | absent | true |
| `clear` for any other `id`, such as `a` beside `ab/c` [States] | absent | `working` | `blocked` | `done` | `error` | `idle` | true |
| A hard reset (RIS), which also hands the status back to `OSC 9;4` [Records and ids] | absent | absent, new | absent, new | absent, new | absent, new | absent | unchanged |
| A soft reset (DECSTR), or a switch between the primary and alternate screen [Records and ids] | absent | `working` | `blocked` | `done` | `error` | `idle` | unchanged |
| A shell prompt, `OSC 133 ; A` with or without parameters [States] | absent | absent, new | absent, new | `done` | `error` | `idle` | unchanged |
| Any other `OSC 133` mark | absent | `working` | `blocked` | `done` | `error` | `idle` | unchanged |
| The process exits [States] | absent | absent, new | absent, new | `done` | `error` | `idle` | unchanged |
| A `working` or `blocked` report for this `id` after the process exited [States] | absent | absent, new | absent, new | absent, new | absent, new | absent | true |
| A report for a 65th `id` while this record is the least recently reported [Limits] | not applicable | absent, new | absent, new | absent, new | absent, new | absent | true |
| A report for a 65th `id` while an older record exists [Limits] | not applicable | `working` | `blocked` | `done` | `error` | `idle` | true |
| A report that breaks a limit or a rule above, such as a 4097-byte sequence or a malformed pair whose key is over 16 bytes [Limits] | absent | `working` | `blocked` | `done` | `error` | `idle` | unchanged |

A session whose program left a `done` record and returned to the shell therefore still reports `done`, while one it left `blocked` no longer reports `input`. `TestProgramRecordLifecycle` in `terminal/status_program_lifecycle_test.go` checks every cell, and `TestOSC7501RecordSurvival` in `vt/osc7501_test.go` checks which records a prompt, an exit, a hard reset and a newer report leave at the root and below it.

### Notifications

`input` and `done` also come from an OSC 9 notification passed through the classifier you install with `WithStatusClassifier`. The engine latches whatever status the classifier returns, so a classifier can also set `working`, `failed`, `warning` or any other value, even after an `OSC 7501` report. For a program that has sent no `OSC 7501` report since the last hard reset (RIS), such a latch holds until a later progress state contradicts it. The active states 1 and 3 and the error state 2 contradict it. The paused state 4 does not, because it starts no work and reports no failure.

For a program that speaks `OSC 7501`, the newer signal wins. A notification the classifier maps after the newest status change sets the status until the next status change, which clears the latch. A status change is a report that changes which record the status comes from, or its state or kind, a clear that leaves no record included. A report that repeats the status, a progress update included, keeps the latch. Use it when a program announces a wait over OSC 9 that its `OSC 7501` report does not show.

Some programs notify when a wait starts and stay silent when it ends. A consumer that sees the end from its own sources reads the latch with `(*SessionManager).StatusLatch(id)`. It returns the latched status, the notification sequence that set it, and whether the session is tracked. `WithdrawStatusLatch(id, want, seq)` clears it as a compare-and-swap. It refuses an unknown session, a `want` other than `input` or `done`, a latch that no longer equals `want`, and a latch a newer notification set after `seq`. A slow poller can therefore never erase a fresh request. The next sweep recomputes the status from the program's own reports alone.

### Status event fields

Each status event also carries:

- `progressValue`, the `progress` of the `OSC 7501` report when it carries one, else the `OSC 9;4` percentage, or -1 when the program reported none. -1 is not 0%.
- `notification` and `notificationSeq` for a fresh OSC 9 message, so a consumer with no classifier still receives it.
- `statusSeq` numbers the occurrence of the session's status. A sweep raises it by one when it emits a changed status, a status a classified notification latched since the last sweep, or an input, done or failed status after an OSC 7501 report since then created a record, changed one's state or kind, or cleared one, and so changed which record the status came from, or its state or kind, at that report. A report keeping a record's state and kind raises it only by changing the status. It never falls for one id; 0, absent on the wire, means nothing numbered yet. This text is the Go doc of `SessionInfo.StatusSeq`, and `TestStatusSeqDocIsQuotedWordForWord` fails when a copy drifts from it.
- `seenSeq`, the highest `statusSeq` any reader acknowledged. A status that calls for attention, such as `done` or `input`, is unread while `statusSeq` is above `seenSeq`.

Both numbers never fall for one session, and both are absent until the first sweep numbers something. The session list carries them too.

### Seen state

`PUT /api/sessions/{id}/seen` with `{"seq": N}` raises the session's `seenSeq` to N, capped at its current `statusSeq`. An ack older than the stored value changes nothing, so a lagging window or device can never hide a newer status. An N above `statusSeq` is accepted and capped, so a 204 confirms the floor reached N only for an N the client received. The route answers 204, including for a no-op, 404 for an unknown session and 400 for a body without an unsigned integer `seq`. The next sweep pushes the new `seenSeq` to every subscriber, so a status read on one device stops counting as unread on all the others. Seen state lives in memory only: a restored session is a new process with a new id and starts with nothing to have seen.

The session list and a new subscriber's first frame carry what the last sweep emitted for every field the sweep derives, so they always agree with the stream. A change such as a process exit reaches them at the next sweep, at most 250 ms later.

### Background activity

`WithSessionActivity(fn)` reports a second value beside the status, for a background task that outlives the turn that started it. `activity` is `""` for none, `working` while a background task runs, `waiting` when it is stopped and resumable with nobody being asked, and `input` when it waits for the user. `activityCount` is the number of sources in that state, 1 or more whenever `activity` is set. Both fields are always present.

The engine calls `fn` once per session on each status sweep. The session list and a new status-stream subscriber's first frame carry the value the last sweep read. `fn` must be safe for concurrent use with the host's own writers, must not block and must not do I/O. Activity never enters the status, never touches the latch, and never sets `reportsActivity`, so a background task does not light the tab's turn indicator. Without the option, every session's activity is empty.

### Titles

The server resolves each session's `title` from four sources, in this order:

1. The name the user pinned.
2. The window title the program set with OSC 0 or OSC 2.
3. A title the client asked the server to remember with `PUT /api/sessions/{id}/title`.
4. The foreground process or the working directory.

Every attached client shows the same label without computing it again. `pinnedTitle` travels beside it, so a client can tell a chosen name from an inferred one.

### Aliases

Each session also has an `alias`, a short public name a browser can put in a page address. It is 1 to 64 characters of letters, digits, `_` and `-`, and no two live sessions share one. The server picks a random 8-character alias when it creates the session, unless `CreateWith` names one that is valid and free. An alias grants nothing: a client attaches with the session `id`, never with the alias.

A host can give a session a better name with `(*SessionManager).SetSessionAlias(id, alias)`. It returns `false` and changes nothing for an unknown session, a value outside that alphabet or length, or a value another live session holds. The create response, the session list and the status stream all carry the alias, and the next status sweep pushes a change to every client.

### Several clients on one session

When several clients share a session, a live resize goes to the last writer. When a client disconnects, the screen relaxes to the smallest size among the clients that remain.

## The processes a session starts

A session's process inherits the server's environment plus a default terminal identity: `TERM=xterm-256color`, `COLORTERM=truecolor`, `TERM_PROGRAM=iTerm.app` and `TERM_PROGRAM_VERSION=3.6.6`. This identity advertises truecolor, `OSC 9;4` progress reporting and DEC 2026 synchronized output, which the engine implements. `WithEnv` values come after these defaults, so your entry for the same variable wins.

### Session reaping

On Linux, a closed session does not leave processes behind. A child that calls `setsid()` leaves its process group and its session, so neither `kill(-pgid)` nor the `SIGHUP` from closing the PTY reaches it. A process moved to init has no ancestry left to follow. Some agent runtimes do exactly this, and some never exit on end of input.

The engine spawns every session with one unguessable environment variable, `WT_SESSION_REAP`. `execve` copies it into every descendant, and it survives both `setsid()` and the move to init. At session end the engine finds every process that still carries it and stops them in four steps. It waits, sends `SIGTERM`, waits again, then sends `SIGKILL`. It logs one `session reap reclaimed escaped processes` line with `survivors`, `term_reclaimed`, `kill_forced` and `resident_bytes`, and only when it had to reclaim something.

This needs no capability, no cgroup, no mount and no PID namespace. In an unprivileged container, reclaiming a `setsid()` escapee took 354 ms, and a full scan of 17,547 processes took about 81 ms. Reaping is always on, with three limits:

- A descendant that `execve`s with a deliberately emptied environment escapes it.
- A tree that forks during teardown can outrun one scan. Each step scans again rather than reusing the first list, and the `SIGKILL` step repeats its scan until it finds no session process, up to eight times. A tree that keeps forking through all eight escapes it.
- A process stuck in an uninterruptible kernel wait, shown as state `D` in `ps`, ends only when that wait does. A hung NFS or FUSE mount can cause this. Teardown stops waiting for it after the eighth scan.

In the last two cases the engine logs `session reap could not end escaped processes` with a `remaining` count.

The engine's value is the only assignment to `WT_SESSION_REAP` in the child environment. It removes that key from your `WithEnv` and from the server's inherited environment before it adds its own. Setting the variable yourself therefore does nothing, and a server started inside one of these sessions does not pass its parent's value on.

`Containment` (`NewContainment`, `WithContainment`) is the stronger boundary, and it is opt-in. It puts each session in its own cgroup, which nothing can escape, and it reports per-session `memory.peak` and `pids.peak`.

### Zombie processes

`StartZombieReaper(log, interval)` solves a separate problem and is also opt-in. Session reaping stops processes that are still alive. The zombie reaper collects the exit status of finished processes nobody waited for.

A server running as its container's PID 1 inherits every orphan in the container. Go's `os/exec` waits only on the children it created, so every language server and every `git` a session started stays behind as a zombie on the server. One container measured 17,323 zombies.

Call `StartZombieReaper` from the composition root of a server that is, or may become, PID 1. It sets `PR_SET_CHILD_SUBREAPER`, so orphans arrive even behind an init shim. It then sweeps every `interval`, 30 seconds when you pass 0 and never less than 1 second, and returns a stop function.

It is a periodic sweep rather than a `SIGCHLD` handler, because `signal.Notify` is process-wide state and the Go runtime uses `SIGCHLD` for `os/exec`. It never waits on a process the engine spawned. A lock held across each spawn guards the list of those processes, so a generic `wait(-1)` cannot take a session's exit status.

### Platforms

The PTY needs a Unix-like system such as Linux or macOS. Session reaping and the zombie reaper do nothing outside Linux. `NewContainment` returns an error there, and the process-based title falls back to the command's name.
