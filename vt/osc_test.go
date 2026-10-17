package vt

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/cplieger/runesafe/v3"
)

func TestOSC2SetsTitleBEL(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b]2;hello\x07"))
	if s.Title != "hello" {
		t.Fatalf("expected title %q, got %q", "hello", s.Title)
	}
}

func TestOSC2SetsTitleST(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b]2;world\x1b\\"))
	if s.Title != "world" {
		t.Fatalf("expected title %q, got %q", "world", s.Title)
	}
}

func TestOSC0SetsTitleAndIcon(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b]0;my title\x07"))
	if s.Title != "my title" {
		t.Fatalf("expected title %q, got %q", "my title", s.Title)
	}
}

// TestOSC1DoesNotSetTitle verifies OSC 1 (set icon name only) does NOT change
// the window title — it must not clobber a title set via OSC 0/2.
func TestOSC1DoesNotSetTitle(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b]2;window\x07"))    // real window title
	s.Write([]byte("\x1b]1;icon name\x07")) // icon name only — must be ignored
	if s.Title != "window" {
		t.Fatalf("OSC 1 clobbered the window title: got %q, want %q", s.Title, "window")
	}
}

func TestUnknownOSCIgnored(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("ABC"))
	// Send an out-of-scope OSC (777 = urxvt notifications): it must be consumed
	// and ignored (dispatchOsc's default case), touching neither the screen nor
	// the title. OSC 52 is a real handler now, so it's covered in features_test.
	s.Write([]byte("\x1b]777;notify;title;body\x07"))
	// Verify screen is not corrupted
	if s.RowString(0) != "ABC" {
		t.Fatalf("screen corrupted after unknown OSC: got %q", s.RowString(0))
	}
	// Title should remain empty
	if s.Title != "" {
		t.Fatalf("title should be empty after unknown OSC, got %q", s.Title)
	}
}

func TestOSCTitleUpdatesOnSubsequentSet(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b]2;first\x07"))
	s.Write([]byte("\x1b]2;second\x1b\\"))
	if s.Title != "second" {
		t.Fatalf("expected title %q, got %q", "second", s.Title)
	}
}

func TestOSCEmptyTitle(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b]2;something\x07"))
	// Set empty title
	s.Write([]byte("\x1b]2;\x07"))
	if s.Title != "" {
		t.Fatalf("expected empty title, got %q", s.Title)
	}
}

func TestOSCAbortedByCANDoesNotCorrupt(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("AB"))
	// Start an OSC but abort with CAN (0x18) before terminator
	s.Write([]byte("\x1b]2;partial\x18"))
	// Title should NOT be set
	if s.Title != "" {
		t.Fatalf("title should be empty after CAN abort, got %q", s.Title)
	}
	// Screen should not be corrupted
	if s.RowString(0) != "AB" {
		t.Fatalf("screen corrupted after CAN abort: got %q", s.RowString(0))
	}
	// A subsequent valid OSC should work
	s.Write([]byte("\x1b]2;valid\x07"))
	if s.Title != "valid" {
		t.Fatalf("expected title %q after recovery, got %q", "valid", s.Title)
	}
}

// TestOSCKeeps0x9CAsPayload verifies a 0x9C byte inside an OSC is a UTF-8
// continuation byte, not an 8-bit ST that would cut the payload.
func TestOSCKeeps0x9CAsPayload(t *testing.T) {
	t.Run("osc8_link_with_U+011C_applies_whole", func(t *testing.T) {
		s := New(2, 40)
		url := "https://example.com/\u011c/rest" // U+011C is C4 9C
		s.Write([]byte("\x1b]8;;" + url + "\x07X"))
		if got := s.Cells[0][0].Hyperlink; got != url {
			t.Errorf("OSC 8 linked %q, want %q", got, url)
		}
		if got := s.RowString(0); got != "X" {
			t.Errorf("RowString(0) = %q, want %q (no payload printed)", got, "X")
		}
	})

	t.Run("osc2_title_with_U+00DC_applies_whole", func(t *testing.T) {
		s := New(2, 40)
		s.Write([]byte("\x1b]2;\u00dcber\x07")) // U+00DC is C3 9C
		if s.Title != "\u00dcber" {
			t.Errorf("OSC 2 title = %q, want %q", s.Title, "\u00dcber")
		}
		if got := s.RowString(0); got != "" {
			t.Errorf("RowString(0) = %q, want empty (no payload printed)", got)
		}
	})
}

// TestOSCAbortedBySUB verifies SUB aborts an OSC without dispatching the title.
func TestOSCAbortedBySUB(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b]2;aborted"))
	s.Write([]byte{0x1A}) // SUB
	if s.pState != stGround {
		t.Fatalf("SUB did not abort OSC: state=%d", s.pState)
	}
	if s.Title == "aborted" {
		t.Fatal("SUB in OSC dispatched title (exit action fired)")
	}
}

// TestOSCAllDigitPayloadNoSeparator verifies an all-digit OSC payload with no
// separator ("2", no ';') is parsed in-bounds, and OSC 2 sets the title to the
// empty (absent) data. Driven through the public parser (ESC ] 2 BEL) rather
// than by hand-seeding the internal OSC buffer.
func TestOSCAllDigitPayloadNoSeparator(t *testing.T) {
	s := New(1, 10)
	s.Title = "prev"
	s.Write([]byte("\x1b]2\x07")) // OSC 2, no ';' separator, terminated by BEL
	if s.Title != "" {
		t.Errorf("OSC 2 with no separator: Title = %q, want \"\" (empty data)", s.Title)
	}
}

// TestOSCMalformedCommandIsIgnored verifies an OSC whose command field is not
// a plain decimal number runs no handler, even one that wraps to a handled id.
func TestOSCMalformedCommandIsIgnored(t *testing.T) {
	cases := []struct{ name, cmd string }{
		{"wraps_to_osc_8", "18446744073709551624;;"},   // 2^64 + 8
		{"wraps_to_osc_2", "18446744073709551618;new"}, // 2^64 + 2
		{"empty_payload", ""},
		{"no_digits", "new"},
		{"trailing_letter", "2x;new"},
		{"signed", "+2;new"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New(2, 10)
			s.Write([]byte("\x1b]2;keep\x07\x1b]8;;https://keep.example\x07A"))
			s.Write([]byte("\x1b]" + tc.cmd + "\x07B"))
			if s.Title != "keep" {
				t.Errorf("Title = %q, want %q (unchanged)", s.Title, "keep")
			}
			if got := s.Cells[0][1].Hyperlink; got != "https://keep.example" {
				t.Errorf("Cells[0][1].Hyperlink = %q, want %q (link still open)", got, "https://keep.example")
			}
		})
	}
}

// TestOSCOmittedCommandIsOSC0 verifies an empty command field before ';' is
// xterm's omitted parameter and sets both titles, as OSC 0 does.
func TestOSCOmittedCommandIsOSC0(t *testing.T) {
	s := New(1, 10)
	s.Write([]byte("\x1b]2;window\x07\x1b]1;icon\x07"))
	s.Write([]byte("\x1b];new\x07"))
	if s.Title != "new" {
		t.Errorf("Title = %q, want %q", s.Title, "new")
	}
	if s.iconTitle != "new" {
		t.Errorf("iconTitle = %q, want %q", s.iconTitle, "new")
	}
}

func TestOSC9NotificationLeavesTitleUnchanged(t *testing.T) {
	s := New(1, 10)
	const keep = "keep"
	s.Title = keep
	s.Write([]byte("\x1b]9;iTerm-only notification\x07"))
	if s.Title != keep {
		t.Errorf("OSC 9 notification: Title = %q, want %q (unchanged)", s.Title, keep)
	}
}

func TestOSC9CapturesNotificationST(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b]9;Task finished\x1b\\"))
	if s.Notification != "Task finished" {
		t.Fatalf("Notification = %q, want %q", s.Notification, "Task finished")
	}
	if s.NotificationSeq != 1 {
		t.Fatalf("NotificationSeq = %d, want 1", s.NotificationSeq)
	}
}

func TestOSC9CapturesNotificationBEL(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b]9;Approval needed\x07"))
	if s.Notification != "Approval needed" {
		t.Fatalf("Notification = %q, want %q", s.Notification, "Approval needed")
	}
}

// A repeated identical message must still advance the sequence so the status
// layer detects the new edge (two "Approval needed" in a row are two events).
func TestOSC9SeqAdvancesOnRepeat(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b]9;Approval needed\x07"))
	s.Write([]byte("\x1b]9;Approval needed\x07"))
	if s.NotificationSeq != 2 {
		t.Fatalf("NotificationSeq = %d, want 2", s.NotificationSeq)
	}
}

// TestOSC9ConEmuProgress verifies the ConEmu progress subcommand (OSC 9 ; 4 ; st)
// is captured into Progress, not into Notification. A program emits it while it
// works, which the status layer maps to a working indicator.
func TestOSC9ConEmuProgress(t *testing.T) {
	s := New(24, 80)
	if s.Progress != -1 {
		t.Fatalf("initial Progress = %d, want -1 (none seen)", s.Progress)
	}
	// Indeterminate progress (state 3): agent working.
	s.Write([]byte("\x1b]9;4;3\x07"))
	if s.Progress != 3 {
		t.Fatalf("after 9;4;3, Progress = %d, want 3", s.Progress)
	}
	if s.Notification != "" || s.NotificationSeq != 0 {
		t.Fatalf("ConEmu progress captured as notification: %q seq=%d", s.Notification, s.NotificationSeq)
	}
	// A state with a percentage field (state 4, 100%): only the state is kept.
	s.Write([]byte("\x1b]9;4;4;100\x07"))
	if s.Progress != 4 {
		t.Fatalf("after 9;4;4;100, Progress = %d, want 4", s.Progress)
	}
	// Cleared (state 0).
	s.Write([]byte("\x1b]9;4;0\x07"))
	if s.Progress != 0 {
		t.Fatalf("after 9;4;0, Progress = %d, want 0", s.Progress)
	}
	// An out-of-range state is ignored (Progress unchanged).
	s.Write([]byte("\x1b]9;4;9\x07"))
	if s.Progress != 0 {
		t.Fatalf("after out-of-range 9;4;9, Progress = %d, want 0 (unchanged)", s.Progress)
	}
}

// TestOSC9NonProgressSubcommandIgnored verifies a numeric OSC 9 subcommand other
// than 4 is neither captured as a notification nor as a progress update.
func TestOSC9NonProgressSubcommandIgnored(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b]9;1;hello\x07"))
	if s.Notification != "" || s.NotificationSeq != 0 {
		t.Fatalf("numeric subcommand captured as notification: %q", s.Notification)
	}
	if s.Progress != -1 {
		t.Fatalf("non-progress subcommand set Progress = %d, want -1", s.Progress)
	}
}

func TestOSC9EmptyIgnored(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b]9;\x07"))
	if s.Notification != "" || s.NotificationSeq != 0 {
		t.Fatalf("empty OSC 9 captured: Notification=%q seq=%d", s.Notification, s.NotificationSeq)
	}
}

func TestSanitizeNotification(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"c0 + del stripped, printable kept", "done\tnow\x7f\n!", "donenow!"},
		{"c1 controls stripped", "a\u0085b\u009cc", "abc"},
		// The classes the hand-rolled C0/C1 loop used to pass (the u07
		// deferral finding): Bidi_Control reordering runes and the JS line
		// terminators must not survive into log lines or status events.
		{"bidi controls stripped", "ok\u202etxt.exe\u202cend", "oktxt.exeend"},
		{"bidi isolates + alm stripped", "\u2066x\u2069\u061cy\u200e\u200f", "xy"},
		{"js line terminators stripped", "one\u2028two\u2029three", "onetwothree"},
		{"empty is empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeNotification(tc.in); got != tc.want {
				t.Errorf("sanitizeNotification(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	// Clamped to maxNotificationLen runes.
	long := strings.Repeat("x", maxNotificationLen+50)
	if got := len([]rune(sanitizeNotification(long))); got != maxNotificationLen {
		t.Fatalf("clamp: rune length = %d, want %d", got, maxNotificationLen)
	}
	// Every surviving rune is safe under the shared policy — the invariant
	// the classifier hook and consumer log attributes rely on.
	for _, r := range sanitizeNotification("a\x1b[31m\u202e\u2028b\u009f") {
		if runesafe.IsUnsafeSingleLine(r) {
			t.Fatalf("unsafe rune %U survived sanitizeNotification", r)
		}
	}
}

func TestIsAllDigits(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"4", true}, {"123", true}, {"", false}, {"4a", false}, {"a", false}, {"1.5", false},
	}
	for _, tc := range cases {
		if got := isAllDigits(tc.in); got != tc.want {
			t.Errorf("isAllDigits(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestOSC5SpecialColorSetQueryReset(t *testing.T) {
	s := New(2, 10)
	// OSC 5 sets special color 0 to pure green.
	s.Write([]byte("\x1b]5;0;rgb:00/ff/00\x07"))
	s.response = nil
	// OSC 5 query reports it back, 16-bit-per-channel.
	s.Write([]byte("\x1b]5;0;?\x07"))
	if got, want := string(s.response), "\x1b]5;0;rgb:0000/ffff/0000\x1b\\"; got != want {
		t.Errorf("OSC 5 query after set = %q, want %q", got, want)
	}
	// OSC 105 resets special color 0; the query then reports the unset default (black).
	s.Write([]byte("\x1b]105;0\x07"))
	s.response = nil
	s.Write([]byte("\x1b]5;0;?\x07"))
	if got, want := string(s.response), "\x1b]5;0;rgb:0000/0000/0000\x1b\\"; got != want {
		t.Errorf("OSC 5 query after OSC 105 reset = %q, want %q", got, want)
	}
}

// TestOSC105ResetAllSpecialColors verifies OSC 105 with no index resets EVERY
// special color, and that a reset on a Screen with none set is a safe no-op
// (the specialColors==nil guard). The existing OSC 105 test only resets one index.
func TestOSC105ResetAllSpecialColors(t *testing.T) {
	s := New(2, 10)
	// Reset-all on a fresh Screen (nothing set) must be a no-op (nil-guard).
	s.Write([]byte("\x1b]105\x07"))
	// Set two special colors, then reset ALL with an empty OSC 105 payload.
	s.Write([]byte("\x1b]5;0;rgb:00/ff/00\x07")) // special color 0 -> green
	s.Write([]byte("\x1b]5;1;rgb:ff/00/00\x07")) // special color 1 -> red
	s.Write([]byte("\x1b]105\x07"))              // reset ALL (no index)
	s.response = nil
	s.Write([]byte("\x1b]5;0;?\x07"))
	s.Write([]byte("\x1b]5;1;?\x07"))
	want := "\x1b]5;0;rgb:0000/0000/0000\x1b\\" + "\x1b]5;1;rgb:0000/0000/0000\x1b\\"
	if got := string(s.response); got != want {
		t.Errorf("after OSC 105 reset-all, queries = %q, want both black %q", got, want)
	}
}

// TestOSC104ResetAllPalette verifies OSC 104 with no index resets the WHOLE
// palette (marking PaletteChanged), and that a reset with no override set is a
// no-op that does NOT mark PaletteChanged (the paletteOverride==nil guard). The
// existing OSC 104 test only resets one index.
func TestOSC104ResetAllPalette(t *testing.T) {
	s := New(2, 10)
	// Reset with no overrides set: nil-guard early return, nothing marked.
	s.Write([]byte("\x1b]104\x07"))
	if s.paletteChanged {
		t.Error("OSC 104 reset with no overrides marked PaletteChanged; want unchanged")
	}
	// Override two indices, then reset the whole palette with an empty payload.
	s.Write([]byte("\x1b]4;1;rgb:00/ff/00\x07"))
	s.Write([]byte("\x1b]4;2;rgb:00/00/ff\x07"))
	s.paletteChanged = false
	s.Write([]byte("\x1b]104\x07"))
	if !s.paletteChanged {
		t.Error("OSC 104 reset-all did not mark PaletteChanged")
	}
	// The override is gone: index 1 no longer queries back as the green override.
	s.response = nil
	s.Write([]byte("\x1b]4;1;?\x07"))
	if got := string(s.response); got == "\x1b]4;1;rgb:0000/ffff/0000\x1b\\" {
		t.Errorf("index 1 override survived OSC 104 reset-all: %q", got)
	}
}

// TestOSC4PaletteIndexBounds walks OSC 4's addressable range end to end: the
// first palette slot, the last special-color register, and the overlap point
// where OSC 4's index 256 and OSC 5's index 0 name the SAME register. A
// rejected endpoint is a slot an application can never set or read back.
func TestOSC4PaletteIndexBounds(t *testing.T) {
	t.Run("first_palette_slot", func(t *testing.T) {
		s := New(2, 10)
		if _, err := s.Write([]byte("\x1b]4;0;rgb:00/ff/00\x07\x1b]4;0;?\x07")); err != nil {
			t.Fatalf("write: %v", err)
		}
		if got, want := string(s.TakeResponse()), "\x1b]4;0;rgb:0000/ffff/0000\x1b\\"; got != want {
			t.Errorf("OSC 4 query of index 0 after set = %q, want %q", got, want)
		}
	})

	t.Run("last_special_color_register", func(t *testing.T) {
		s := New(2, 10)
		if _, err := s.Write([]byte("\x1b]4;263;?\x07")); err != nil {
			t.Fatalf("write: %v", err)
		}
		if got, want := string(s.TakeResponse()), "\x1b]4;263;rgb:0000/0000/0000\x1b\\"; got != want {
			t.Errorf("OSC 4 query of index 263 = %q, want %q (unset register reports black)", got, want)
		}
	})

	t.Run("index_256_is_special_color_0", func(t *testing.T) {
		s := New(2, 10)
		// Set through OSC 4's special-color range, read back through OSC 5.
		if _, err := s.Write([]byte("\x1b]4;256;rgb:00/ff/00\x07\x1b]5;0;?\x07")); err != nil {
			t.Fatalf("write: %v", err)
		}
		if got, want := string(s.TakeResponse()), "\x1b]5;0;rgb:0000/ffff/0000\x1b\\"; got != want {
			t.Errorf("OSC 5 query of special color 0 after an OSC 4;256 set = %q, want %q", got, want)
		}
	})

	t.Run("trailing_index_without_a_spec_is_ignored", func(t *testing.T) {
		s := New(2, 10)
		// Untrusted output can end a pair list mid-pair; the dangling index has
		// no spec to read.
		if _, err := s.Write([]byte("\x1b]4;1;rgb:00/ff/00;2\x07\x1b]4;1;?\x07")); err != nil {
			t.Fatalf("write: %v", err)
		}
		if got, want := string(s.TakeResponse()), "\x1b]4;1;rgb:0000/ffff/0000\x1b\\"; got != want {
			t.Errorf("OSC 4 query after a pair list with a dangling index = %q, want %q", got, want)
		}
	})
}

// TestOSC5SpecialColorIndexBounds is the same walk for OSC 5's own numbering:
// register 7 is the last one, 8 does not exist, and register k must be the same
// storage OSC 4 reaches at 256+k.
func TestOSC5SpecialColorIndexBounds(t *testing.T) {
	t.Run("last_register", func(t *testing.T) {
		s := New(2, 10)
		if _, err := s.Write([]byte("\x1b]5;7;rgb:00/00/ff\x07\x1b]5;7;?\x07")); err != nil {
			t.Fatalf("write: %v", err)
		}
		if got, want := string(s.TakeResponse()), "\x1b]5;7;rgb:0000/0000/ffff\x1b\\"; got != want {
			t.Errorf("OSC 5 query of register 7 after set = %q, want %q", got, want)
		}
	})

	t.Run("past_the_last_register_is_ignored", func(t *testing.T) {
		s := New(2, 10)
		if _, err := s.Write([]byte("\x1b]5;8;?\x07")); err != nil {
			t.Fatalf("write: %v", err)
		}
		if got := string(s.TakeResponse()); got != "" {
			t.Errorf("OSC 5 query of register 8 = %q, want no reply (there are 8 registers, 0..7)", got)
		}
	})

	t.Run("register_k_is_osc4_index_256_plus_k", func(t *testing.T) {
		s := New(2, 10)
		if _, err := s.Write([]byte("\x1b]5;1;rgb:ff/00/00\x07\x1b]4;257;?\x07")); err != nil {
			t.Fatalf("write: %v", err)
		}
		if got, want := string(s.TakeResponse()), "\x1b]4;257;rgb:ffff/0000/0000\x1b\\"; got != want {
			t.Errorf("OSC 4 query of index 257 after an OSC 5;1 set = %q, want %q", got, want)
		}
	})

	t.Run("trailing_index_without_a_spec_is_ignored", func(t *testing.T) {
		s := New(2, 10)
		if _, err := s.Write([]byte("\x1b]5;0;rgb:00/ff/00;1\x07\x1b]5;0;?\x07")); err != nil {
			t.Fatalf("write: %v", err)
		}
		if got, want := string(s.TakeResponse()), "\x1b]5;0;rgb:0000/ffff/0000\x1b\\"; got != want {
			t.Errorf("OSC 5 query after a pair list with a dangling index = %q, want %q", got, want)
		}
	})
}

// TestOSCResetsAddressTheRequestedIndex: OSC 104 and OSC 105 must clear exactly
// the index named, at both ends of each range. A reset that misses leaves an
// application's override in place for the rest of the session.
func TestOSCResetsAddressTheRequestedIndex(t *testing.T) {
	cases := []struct {
		name  string
		write string
		query string
		want  string
	}{
		{
			name:  "palette_index_0",
			write: "\x1b]4;0;rgb:00/ff/00\x07\x1b]104;0\x07",
			query: "\x1b]4;0;?\x07",
			want:  "\x1b]4;0;rgb:0000/0000/0000\x1b\\", // the palette's own slot 0
		},
		{
			name:  "palette_index_255",
			write: "\x1b]4;255;rgb:00/ff/00\x07\x1b]104;255\x07",
			query: "\x1b]4;255;?\x07",
			want:  "\x1b]4;255;rgb:eeee/eeee/eeee\x1b\\", // the grayscale ramp's top
		},
		{
			name:  "special_color_1",
			write: "\x1b]5;1;rgb:ff/00/00\x07\x1b]105;1\x07",
			query: "\x1b]5;1;?\x07",
			want:  "\x1b]5;1;rgb:0000/0000/0000\x1b\\",
		},
		{
			name:  "special_color_7",
			write: "\x1b]5;7;rgb:ff/00/00\x07\x1b]105;7\x07",
			query: "\x1b]5;7;?\x07",
			want:  "\x1b]5;7;rgb:0000/0000/0000\x1b\\",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New(2, 10)
			if _, err := s.Write([]byte(tc.write)); err != nil {
				t.Fatalf("write: %v", err)
			}
			s.TakeResponse()
			if _, err := s.Write([]byte(tc.query)); err != nil {
				t.Fatalf("query: %v", err)
			}
			if got := string(s.TakeResponse()); got != tc.want {
				t.Errorf("query after reset = %q, want %q (the override must be gone)", got, tc.want)
			}
		})
	}
}

// TestParseXColorAcceptsEveryDigitCount: both X11 color forms accept 1 to 4 hex
// digits per channel, and the two forms differ in how they widen a short one —
// "#" left-justifies into a 16-bit field, "rgb:" scales proportionally. The
// 4-digit forms are what a terminal itself emits, so rejecting them would break
// feeding one terminal's reply back into another.
func TestParseXColorAcceptsEveryDigitCount(t *testing.T) {
	cases := []struct {
		name string
		spec string
		want string
	}{
		{name: "hash_one_digit_per_channel", spec: "#f00", want: "\x1b]4;1;rgb:f0f0/0000/0000\x1b\\"},
		{name: "hash_two_digits_per_channel", spec: "#ff0000", want: "\x1b]4;1;rgb:ffff/0000/0000\x1b\\"},
		{name: "hash_four_digits_per_channel", spec: "#ffff00000000", want: "\x1b]4;1;rgb:ffff/0000/0000\x1b\\"},
		{name: "rgb_one_digit_per_channel", spec: "rgb:f/0/0", want: "\x1b]4;1;rgb:ffff/0000/0000\x1b\\"},
		{name: "rgb_four_digits_per_channel", spec: "rgb:ffff/0000/0000", want: "\x1b]4;1;rgb:ffff/0000/0000\x1b\\"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New(2, 10)
			if _, err := s.Write([]byte("\x1b]4;1;" + tc.spec + "\x07\x1b]4;1;?\x07")); err != nil {
				t.Fatalf("write: %v", err)
			}
			if got := string(s.TakeResponse()); got != tc.want {
				t.Errorf("OSC 4 query after setting index 1 to %q = %q, want %q", tc.spec, got, tc.want)
			}
		})
	}
}

// TestOSCDynamicColorAdvancesOneSlotPerSpec: a dynamic-color OSC carrying
// several specs walks UP the slot numbers from the one it names (xterm: OSC 10
// with two specs addresses 10 then 11), and stops at slot 19, the last dynamic
// color defined.
func TestOSCDynamicColorAdvancesOneSlotPerSpec(t *testing.T) {
	t.Run("second_spec_addresses_the_next_slot", func(t *testing.T) {
		s := New(2, 10)
		// Slot 10 is the default foreground, 11 the default background.
		if _, err := s.Write([]byte("\x1b]10;#ff0000;#00ff00\x07")); err != nil {
			t.Fatalf("write: %v", err)
		}
		s.TakeResponse()
		if _, err := s.Write([]byte("\x1b]10;?;?\x07")); err != nil {
			t.Fatalf("query: %v", err)
		}
		want := "\x1b]10;rgb:ffff/0000/0000\x1b\\" + "\x1b]11;rgb:0000/ffff/0000\x1b\\"
		if got := string(s.TakeResponse()); got != want {
			t.Errorf("OSC 10 two-spec query = %q, want %q", got, want)
		}
	})

	t.Run("the_walk_stops_after_the_last_slot", func(t *testing.T) {
		s := New(2, 10)
		if _, err := s.Write([]byte("\x1b]19;#0000ff\x07")); err != nil {
			t.Fatalf("write: %v", err)
		}
		s.TakeResponse()
		// Two specs from slot 19: the first is slot 19, the second would be 20.
		if _, err := s.Write([]byte("\x1b]19;?;?\x07")); err != nil {
			t.Fatalf("query: %v", err)
		}
		want := "\x1b]19;rgb:0000/0000/ffff\x1b\\"
		if got := string(s.TakeResponse()); got != want {
			t.Errorf("OSC 19 two-spec query = %q, want only the slot-19 reply %q", got, want)
		}
	})
}

// oscOfLen returns an OSC 8 sequence whose payload ("8;;" plus the URL) is n
// bytes long, and that URL.
func oscOfLen(t *testing.T, n int) (seq, url string) {
	t.Helper()
	url = "https://example.com/" + strings.Repeat("a", n-len("8;;https://example.com/"))
	if got := len("8;;" + url); got != n {
		t.Fatalf("fixture payload is %d bytes, want %d", got, n)
	}
	return "\x1b]8;;" + url + "\x07", url
}

// osc52OfLen returns an OSC 52 set whose payload ("52;;" plus the base64
// text) is n bytes long, and the decoded text. n-4 must be a multiple of 4.
func osc52OfLen(t *testing.T, n int) (seq, text string) {
	t.Helper()
	text = strings.Repeat("x", (n-len("52;;"))/4*3)
	payload := "52;;" + base64.StdEncoding.EncodeToString([]byte(text))
	if len(payload) != n {
		t.Fatalf("fixture payload is %d bytes, want %d", len(payload), n)
	}
	return "\x1b]" + payload + "\x07", text
}

func TestOSCOverCapIsDiscardedWhole(t *testing.T) {
	t.Run("osc52_over_cap_sets_no_clipboard", func(t *testing.T) {
		s := New(2, 10)
		seq, _ := osc52OfLen(t, maxOSCLen+4)
		s.Write([]byte(seq))
		if got := s.TakeClipboard(); got != nil {
			t.Errorf("over-cap OSC 52 queued %d clipboard bytes, want none", len(got))
		}
	})

	t.Run("osc52_at_cap_sets_the_clipboard", func(t *testing.T) {
		s := New(2, 10)
		seq, text := osc52OfLen(t, maxOSCLen)
		s.Write([]byte(seq))
		if got := string(s.TakeClipboard()); got != text {
			t.Errorf("at-cap OSC 52 queued %d bytes, want the %d-byte text", len(got), len(text))
		}
	})

	t.Run("osc8_over_cap_sets_no_link", func(t *testing.T) {
		s := New(2, 10)
		seq, _ := oscOfLen(t, maxOSCLen+1)
		s.Write([]byte(seq + "X"))
		if got := s.Cells[0][0].Hyperlink; got != "" {
			t.Errorf("over-cap OSC 8 linked the cell to a %d-byte URL, want no link", len(got))
		}
	})

	t.Run("osc8_at_cap_sets_the_link", func(t *testing.T) {
		s := New(2, 10)
		seq, url := oscOfLen(t, maxOSCLen)
		s.Write([]byte(seq + "X"))
		if got := s.Cells[0][0].Hyperlink; got != url {
			t.Errorf("at-cap OSC 8 linked a %d-byte URL, want the %d-byte one", len(got), len(url))
		}
	})

	t.Run("osc_after_an_overflowed_one_applies", func(t *testing.T) {
		s := New(2, 10)
		over52, _ := osc52OfLen(t, maxOSCLen+4)
		over8, _ := oscOfLen(t, maxOSCLen+1)
		s.Write([]byte(over52 + "\x1b]52;;aGVsbG8=\x07")) // base64("hello")
		if got := string(s.TakeClipboard()); got != "hello" {
			t.Errorf("OSC 52 after an overflowed one queued %q, want %q", got, "hello")
		}
		s.Write([]byte(over8 + "\x1b]8;;https://example.com\x07X"))
		if got := s.Cells[0][0].Hyperlink; got != "https://example.com" {
			t.Errorf("OSC 8 after an overflowed one linked %q, want %q", got, "https://example.com")
		}
	})

	t.Run("osc_after_an_aborted_overflowed_one_applies", func(t *testing.T) {
		for name, abort := range map[string]string{"CAN": "\x18", "SUB": "\x1a"} {
			t.Run(name, func(t *testing.T) {
				s := New(2, 10)
				over52, _ := osc52OfLen(t, maxOSCLen+4)
				s.Write([]byte(strings.TrimSuffix(over52, "\x07") + abort + "\x1b]52;;aGVsbG8=\x07"))
				if got := string(s.TakeClipboard()); got != "hello" {
					t.Errorf("OSC 52 after an overflowed one aborted by %s queued %q, want %q", name, got, "hello")
				}
			})
		}
	})
}
