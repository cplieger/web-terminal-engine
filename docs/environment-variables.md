# Environment variable names for servers

This page lists the setting names that servers built on this engine share. It is for a developer building a server on the engine who wants its settings to match the ones operators already know.

The engine reads no environment itself. Your server parses its own configuration and passes options in. Servers that answer the same question still use the same name, so an operator who runs one already knows the others. [web-terminal-server](https://github.com/cplieger/web-terminal-server) and [web-terminal-kiro](https://github.com/cplieger/web-terminal-kiro) both read these names.

| Variable | What the server does with it |
| --- | --- |
| `LISTEN_ADDR` | The listen address, `host:port`. |
| `WORK_DIR` | The working directory of the process the PTY runs. |
| `SCROLLBACK` | The retained history depth, the engine's own `ScrollbackEnvVar`. Pass it through `ClampScrollbackCapacity`. |
| `ALLOWED_HOSTS` | The exact `Host` values to serve, which protects against DNS rebinding. Unset allows any host. |
| `TRUSTED_PROXIES` | The CIDRs or IPs whose forwarded-for header may name the real client. |
| `LOG_LEVEL` | The log level at startup. |

The names carry no prefix. An operator setting one knows the app they run, not the library that serves its terminal, so a component prefix would name something they never chose.

The variable the engine adds to a session's child environment is different and keeps its prefix. `WT_SESSION_REAP`, described in [Sessions and processes](sessions.md#session-reaping), is not an operator setting. It lands in one shared namespace beside everything the system and the user's shell set, so there the prefix prevents a collision.

Two rules keep the list useful:

- Share a name only when the behavior is the same. A setting one server reads and another ignores is a coincidence, so give a server-specific setting a server-specific name.
- The engine reads none of these, so adopting a name is your server's decision. The list is a convention rather than an interface, and nothing translates a different name for you.
