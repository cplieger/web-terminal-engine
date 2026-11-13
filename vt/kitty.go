package vt

import "fmt"

// Kitty keyboard protocol (https://sw.kovidgoyal.net/kitty/keyboard-protocol/):
// negotiation is parsed here, and the client encodes keys from KeyboardFlags.
// Only disambiguate (0x1) is honored: 0x2 needs key-release events, 0x4
// codepoints the browser hides, and 0x8 and 0x10 conflict with the client's
// IME input. Flags are masked on store, so a query reports only what is
// honored, as the spec permits; a new flag needs this mask and the client
// encoder together.
const (
	kbdDisambiguate uint8 = 1 << 0 // 0x1  disambiguate escape codes

	// kbdHonoredMask is the set of enhancement flags this terminal implements.
	kbdHonoredMask = kbdDisambiguate // 0x01

	// maxKbdStack caps each screen's stack, as the spec asks; a full push evicts
	// the oldest entry.
	maxKbdStack = 16
)

// kbdProtocol holds one screen's kitty keyboard-protocol state: the current
// progressive-enhancement flags (masked to kbdHonoredMask) and the push/pop
// stack of previously-current values. The main and alternate screens keep
// independent instances (the spec requires separate stacks per screen) so an
// editor can enable the protocol on the alt screen without disturbing the
// shell's main-screen mode.
type kbdProtocol struct {
	stack []uint8
	flags uint8
}

// activeKbd returns the keyboard-protocol state for the screen currently in
// effect, so every CSI-u operation and the KeyboardFlags accessor address the
// right stack.
func (s *Screen) activeKbd() *kbdProtocol {
	if s.InAltScreen {
		return &s.altKbd
	}
	return &s.mainKbd
}

// KeyboardFlags reports the active screen's current kitty keyboard
// progressive-enhancement flags (0 when the protocol is disabled). The terminal
// layer syncs this to the client so its key encoder switches between legacy and
// kitty CSI-u encoding; switching screens flips to the other stack's flags,
// which the client observes as a modes change.
func (s *Screen) KeyboardFlags() uint8 {
	return s.activeKbd().flags
}

// reportKeyboardFlags answers the query CSI ? u with CSI ? flags u, reporting
// the active screen's current honored flags. Always answered (it leaks no
// screen contents), so a probing app that also requests device attributes gets
// the reply before the DA answer and thus detects protocol support.
func (s *Screen) reportKeyboardFlags() {
	s.response = fmt.Appendf(s.response, "\x1b[?%du", s.activeKbd().flags)
}

// pushKeyboardFlags implements CSI > flags u: save the current flags onto the
// stack and make the given (masked) flags current. The stack is bounded; when
// full the oldest entry is evicted (DoS guard, per spec).
func (s *Screen) pushKeyboardFlags(flags int) {
	k := s.activeKbd()
	if len(k.stack) >= maxKbdStack {
		k.stack = k.stack[1:] // evict the oldest entry
	}
	k.stack = append(k.stack, k.flags)
	k.flags = uint8(flags) & kbdHonoredMask //nolint:gosec // masked to 0x01
}

// popKeyboardFlags implements CSI < number u: pop n entries, restoring the flags
// present before the corresponding pushes. Popping past the bottom empties the
// stack and resets the flags to 0 (per spec).
func (s *Screen) popKeyboardFlags(n int) {
	k := s.activeKbd()
	if n < 1 {
		n = 1
	}
	for ; n > 0 && len(k.stack) > 0; n-- {
		k.flags = k.stack[len(k.stack)-1]
		k.stack = k.stack[:len(k.stack)-1]
	}
	if n > 0 {
		// Popped past the bottom of the stack: reset all flags.
		k.flags = 0
	}
}

// setKeyboardFlags implements CSI = flags ; mode u: modify the active screen's
// current flags in place. mode 3 resets the given bits, mode 2 sets them, and
// mode 1 (the default) replaces all bits with the given set. Flags are masked
// to the honored subset first.
func (s *Screen) setKeyboardFlags(flags, mode int) {
	k := s.activeKbd()
	f := uint8(flags) & kbdHonoredMask //nolint:gosec // masked to 0x01
	switch mode {
	case 3: // reset the given bits
		k.flags &^= f
	case 2: // set the given bits
		k.flags |= f
	default: // mode 1 (or unspecified): given bits set, all others reset
		k.flags = f
	}
}
