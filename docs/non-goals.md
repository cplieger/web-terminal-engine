# Unsupported VT features

This page lists the VT and DEC features the engine leaves out on purpose, and the device queries it answers. It is for a developer who checks whether a program will work in it, or who is about to report a missing sequence.

## Left out on purpose

The engine reads the bytes of these sequences and does nothing with them. They are never echoed or half-drawn.

Two rows leave out less than their name suggests. For window geometry, only the size change is left out. DECCOLM's clear and home effects work, and so do XTWINOPS operations 22 and 23 for the title stack and 18 to 21 for the size and label reports. For X11 colors, the `rgb:` and `#hex` forms work, as do setting, querying and resetting the palette and the dynamic colors.

| Category | Sequences | Reason |
| --- | --- | --- |
| Double-width and double-height lines | DECDWL, DECDHL | Needs a line-level rendering attribute and renderer changes, for a VT220 feature modern programs do not use. |
| Programmatic resize and window geometry | DECCOLM 132-column width change, XTWINOPS window resize, move, iconify and maximize, DECSLPP, DECSNLS | The browser viewport and the PTY own the terminal size, and a browser tab has no window to move. |
| DCS device control | tmux control-mode passthrough | Not modeled. DECRQSS and the XTGETTCAP color-count query share the same DCS parser and work, as described below. |
| Graphics protocols | Sixel, ReGIS, the kitty image protocol, iTerm inline images | Needs a separate rendering path that a DOM renderer cannot host. |
| National character sets | Every national replacement set except UK. DEC Special Graphics, UK, which maps `#` to `£`, and ASCII work. | An older internationalization method that UTF-8 replaced. Modern programs do not send them. |
| Rare SGR attributes | Fonts 10 to 20, framed and encircled (51, 52, 54), superscript and subscript (73 to 75), ideogram (60 to 65) | Modern programs do not use them, and standard monospace fonts cannot show them. |
| X11 Xcms color specifications | CIE Lab, Luv, XYZ, uvY and xyY, rgbi intensity and TekHVC in OSC 4, 5 and 10 to 19 | X11 device colorimetry rather than part of the VT and ANSI standards, and command-line tools do not send them. |
| Joined emoji | Zero-width joiner sequences are not combined into one cell | Needs full grapheme segmentation. Single emoji render correctly, and only joined sequences such as family emoji and skin-tone modifiers may misalign. |

## Device queries the engine answers

Several reports and queries that share the DCS or CSI parsers work:

- DECRQSS (`DCS $ q … ST`) answers for SGR (`m`), the scroll region (`r`), the cursor style (`SP q`), protection (`" q`), the conformance level (`" p`), the left and right margins (`s`) and lines per page and screen (`t`, `* |`). Anything else gets `DCS 0 $ r ST`.
- XTGETTCAP (`DCS + q … ST`) answers the color-count capability with 256.
- DECRQCRA, the rectangle checksum, and the OSC 52 clipboard read-back are answered only when `Screen.AllowScreenReport` is on. Both put their reply into the PTY, so they are off by default.

The [esctest2](https://github.com/ThomasDickey/esctest2) conformance suite checks the VT model. [CONTRIBUTING](../CONTRIBUTING.md) shows how to run it.

## The kitty keyboard protocol

The [progressive enhancement](https://sw.kovidgoyal.net/kitty/keyboard-protocol/) negotiation works. The engine answers the `CSI ? u` query, and it keeps a flag stack per screen that `CSI > u` pushes, `CSI < u` pops and `CSI = u` sets. A program that asks for keyboard enhancement, such as one built on crossterm, detects the support.

Only the disambiguate flag, `0x1`, is honored. The current flags go to the client, which then sends unambiguous `CSI u` key events for Escape, Ctrl and Alt combinations, function keys and the keypad's `KP_*` navigation codes, while plain text still arrives as text.

The engine masks off four other flags. They are report event types (`0x2`), report alternate keys (`0x4`), report all keys (`0x8`) and report associated text (`0x10`). The last two cannot work with the browser's hidden-textarea and IME input. The query reports only the honored flag, so a program that needs another one sees the gap and falls back. The kitty image protocol is a separate feature, and it is not supported.
