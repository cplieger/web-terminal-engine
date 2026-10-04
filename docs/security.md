# Security for embedders

This page covers what the engine protects on its own and what your server must add. It is for a developer who exposes a terminal socket, which is an interactive shell for whoever opens it.

## What your server must add

The engine authenticates no one. It checks that a WebSocket comes from an allowed page origin, and nothing else. Put your own login in front of every route it serves, or keep the server on a private network.

`WithCreateGate(mw)` wraps the whole session REST handler in your own middleware, so the middleware must pick out the create request itself. Each `POST /api/sessions` starts a process, so a rate limit on that request is worth having.

## Cross-origin access

The engine allows same-origin connections only, by default. That default matters. A WebSocket handshake is a `GET`, and `net/http.CrossOriginProtection`, the standard library's CSRF middleware, lets `GET`, `HEAD` and `OPTIONS` through as safe methods. It therefore never checks the upgrade. An app-level cross-origin middleware does not protect this socket, and the engine's own check is the only gate.

To embed a terminal in a page served from another origin, build one policy and pass it to both the handler and the manager:

```go
policy, invalid := terminal.NewOriginPolicy("https://embed.example.com")
if len(invalid) > 0 {
    log.Printf("ignoring malformed allowed origins: %v", invalid)
}

h := terminal.NewHandler(cmd, terminal.WithOriginPolicy(policy))
mgr := terminal.NewSessionManager(factory, terminal.WithManagerOriginPolicy(policy))
```

Each entry is a complete origin, with a scheme, a host and an optional port and nothing else. Matching is exact and ignores case. The scheme's default port is dropped, the way a browser's own `Origin` header drops it.

`NewOriginPolicy` reports a malformed entry instead of storing it, so a typo cannot widen or narrow the policy without notice. A list where every entry is malformed leaves same-origin only.

The policy has no wildcards, accepts no `Origin: null`, and cannot be switched off. A wildcard pattern would make `*` mean "allow everything". A sandboxed iframe and a `file://` page both send `null`.

`(*OriginPolicy).Allows(r)` applies the same decision to your own routes. `Active()` reports whether the policy allows anything beyond same-origin, for a startup log line.

## Session ids are credentials

A session id lets whoever holds it attach to the session and resume it. The engine treats it as a secret in two places.

In logs, pass every session id through `LogID(id)`. It keeps the first 8 bytes, cut back to a whole UTF-8 character, and adds an ellipsis. A client can send any bytes as a resume id, and a plain byte prefix could put invalid UTF-8 into your logs.

In caches, the engine sets `Cache-Control: no-store` on the whole session surface:

- The session JSON bodies and the status stream carry an id in full.
- Every other REST response carries the id in its URL. That includes the responses no handler writes, which are the router's 404, its 405 and its path-cleaning redirect.
- A refusal from your `WithCreateGate` middleware carries the header too.

A `Cache-Control` header your own outer middleware already set stays as it is. That is how you state a stricter policy, or deliberately cache one of these routes. The JSON bodies are the exception and always say `no-store`, because the id is in the body.

## Command lines in logs

The engine logs one line when a session's process starts, and its `command` attribute holds the child's arguments. It is the only log line that carries them. When your arguments can contain a secret, such as a flag filled in from a setting, pass `WithCommandLogValue("[redacted]")`. The line then records that fixed value instead. An empty value is ignored and keeps the default.

## Replies that write into the PTY

Two terminal features send their answer back into the program's input. They are the DECRQCRA rectangle checksum and the OSC 52 clipboard read-back. Both are answered only when `Screen.AllowScreenReport` is enabled, and they are off by default.
