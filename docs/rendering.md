# Colors, links and input

This page covers how the engine picks colors, decides how many cells a character takes, links URLs in a program's output, and turns keys, taps and mouse gestures into terminal input. It is for a developer who themes the terminal or wires input by hand.

## Colors

The 256-color cube and the grayscale ramp follow the xterm formula, and a truecolor run is the RGB value the program sent. The 16 base colors, SGR 30 to 37 and 90 to 97 plus their background forms 40 to 47 and 100 to 107, are different. No standard assigns them RGB values, so every terminal chooses its own.

This engine uses [kitty's published default palette](https://github.com/kovidgoyal/kitty/blob/master/kitty/options/definition.py), because kitty's default background is pure black, which is what the reference UI renders on. Colors resolve on the server, so a run reaches the browser as a resolved `0xRRGGBB` value. A program can override any slot at runtime with OSC 4. `WithTheme` sets your own palette.

### Minimum contrast

Because colors resolve on the server, a program picks a palette slot without knowing the RGB value you gave it. It cannot tell whether its choice is readable on your background, so the terminal has to handle legibility:

```go
h := terminal.NewHandler(cmd, terminal.WithMinimumContrast(4.5))
```

A foreground below the floor blends toward white or black until it reaches that WCAG contrast ratio against its own run's background. The ratio is clamped to 1 to 21, and 1 turns the floor off. It is off by default, so an engine upgrade never recolors an existing setup. 4.5 is the WCAG AA floor for body text. It is also the default of VS Code's integrated terminal, which sets it through xterm.js's `minimumContrastRatio`. iTerm2's Minimum Contrast setting does the same job.

The floor never does four things:

- It never changes a background.
- It never changes the default foreground, which your CSS owns and swaps under DECSCNM reverse video.
- It never reveals concealed text, SGR 8.
- It never changes what an OSC 4 query reports. A program reads back the entry it set, not the value painted over it.

Use the floor together with a palette that suits your background. The palette keeps the adjustment rare. The floor covers three cases no palette can, which are a program's own OSC 4 overrides, the dark corners of the 256-color cube, and truecolor a program chose blind.

## Character width

The `vt` screen follows Unicode's East Asian Width rules, [UAX #11](https://www.unicode.org/reports/tr11/). Wide and fullwidth characters, such as Chinese, Japanese and Korean text, take two cells. A single emoji shown in emoji style also takes two cells. Ambiguous-width characters take one cell, and combining marks take none. Joined emoji sequences are the exception, as [Unsupported VT features](non-goals.md) describes.

## Links

A bare `http://` or `https://` URL in a program's output reaches the browser already linked. The `vt` package finds it on the server while it renders a row, and marks the covered runs with the full URL and the `AttrAutolink` bit, 1024. A URL the terminal wrapped across rows, or one the program wrapped itself with its own indent, is one link with one target rather than a fragment per row.

A link the program set itself with OSC 8 wins and is never overwritten. The client underlines the two differently. An automatic link covers exactly the matched text, so it is underlined all the time. An OSC 8 link is underlined on hover.

The scan looks at the four rows of a wrap chain nearest the row it renders, which keeps the work per row constant. Four rows cover any real URL down to about 40 columns.

A longer URL links imperfectly rather than not at all. Rows near its start link to a URL cut at the edge of the window, and rows far enough past the start carry no link, because the scheme has left the window. At 20 columns, a 100-character URL wraps to five rows. The first three link to its first 80 characters, and the last two are not clickable. Widening the window would cost time on every row of every render, so the limit stays.

## Keyboard

The `keyboard` namespace turns a `KeyboardEvent` into the bytes a PTY expects. `mapKeyboardEvent(ev, modes)` returns either bytes to send or a local action, such as a scroll. `bracketTextForPaste(text, modes)` and `prepareTextForTerminal` do the same for pasted text. Every function that depends on a mode takes the mode state as an argument, and an engine's `modes` satisfies it. The module touches no DOM and sends nothing.

It honors application cursor keys, application keypad, bracketed paste, and the kitty keyboard protocol's disambiguate flag. When an application turns that protocol on, keys arrive as `CSI u` events. [Unsupported VT features](non-goals.md) lists which kitty flags are honored.

## Mobile key toolbar

The `toolbar` namespace is the engine's one DOM widget. `bindMobileToolbar({ toolbar, send, modes, ids? })` wires an existing container's Ctrl, arrow, Tab, Enter and Esc buttons to a send function. Ctrl is sticky. The arrows read the mode state, so a toolbar key sends the same bytes as the physical key. It returns a `MobileToolbarController`, and `DEFAULT_TOOLBAR_IDS` names the default button ids.

## Mouse

`createMouseController` encodes SGR 1006 mouse reports while the application has mouse tracking on. Two optional members of its `MouseInputHandler` set the coordinate frame. `gridElement` names the element whose box is the grid, and `gridSize` gives the grid's size on screen, `renderer.gridSize`. Every report is clamped into that grid.

Mouse input is best-effort, and `sendReport` returns whether a report reached a live socket. Wire it to `connection.sendEphemeral`, which sends only on a live socket and never queues a report for after a reconnect. A click made before a disconnect is therefore not replayed after the screen has been repainted. Four rules follow:

- A release is sent only for a press that was delivered.
- A drag reports a held button only while the application believes that button is down.
- After a reconnect, `resyncGesture()` releases a button left pressed. The connection calls it once the server has declared its capabilities. A gesture whose tracking mode has since been turned off is dropped instead, because a release nobody asked for reaches the program as typed input.
- Motion is coalesced to one report per animation frame, the latest one. Press, release and wheel are never coalesced, because a wheel report is a step count and dropping one loses scroll distance.

`disarmGesture()` forgets a held gesture without reporting anything. Call it when you point one terminal's socket at a different session, so that session is not sent a release for a press it never saw.

## Focus

Focus is not mouse input. `connection.setClientFocus(focused)` reports whether the terminal widget has focus, and the server works out the DEC 1004 focus answer. It uses the attachment state, the report of every attached client, and its own `WithKeepUnfocused` setting. No client writes the focus bytes itself, because one client cannot answer for a session two devices share. A server too old to declare this capability gets no report.

## Scrolling

`createScrollController` follows the output while the reader is at the bottom and holds the view while they read scrollback. It also has two seams for the renderer:

- `noteContentShrink(scrollTopBefore)` says that a removed row, not a gesture, caused the scroll event about to arrive.
- `reconcileScrollRange()` moves the offset back inside the container's range when the browser did not do it after the content shrank.

The second exists because a browser is not required to clamp an offset the content shrank under. Blink and Gecko do it during layout, and WebKit does not. Without the correction, an iOS view stays past the end of the content after an application clears the screen, until the reader scrolls.
