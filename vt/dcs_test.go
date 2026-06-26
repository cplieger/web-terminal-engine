package vt

import (
	"strings"
	"testing"
)

// TestDECRQSS_SGRDefault verifies a DECRQSS SGR query on a default style
// returns the SGR-0 status string.
func TestDECRQSS_SGRDefault(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1bP$qm\x1b\\"))
	want := "\x1bP1$r0m\x1b\\"
	if got := string(s.Response); got != want {
		t.Errorf("DECRQSS SGR default = %q, want %q", got, want)
	}
}

// TestDECRQSS_SGRReflectsStyle verifies the SGR status string reflects the
// current style (bold + red).
func TestDECRQSS_SGRReflectsStyle(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b[1;31m")) // bold + red
	s.Write([]byte("\x1bP$qm\x1b\\"))
	want := "\x1bP1$r0;1;31m\x1b\\"
	if got := string(s.Response); got != want {
		t.Errorf("DECRQSS SGR = %q, want %q", got, want)
	}
}

// TestDECRQSS_SGRAllAttributesPresent verifies every set attribute appears in
// the SGR status string when many attributes are enabled at once.
func TestDECRQSS_SGRAllAttributesPresent(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b[1;2;3;4;5;7;8;9;53m"))
	s.Response = nil
	s.Write([]byte("\x1bP$qm\x1b\\"))
	got := string(s.Response)
	if !strings.HasPrefix(got, "\x1bP1$r") || !strings.HasSuffix(got, "m\x1b\\") {
		t.Fatalf("response format: %q", got)
	}
	params := got[len("\x1bP1$r") : len(got)-len("m\x1b\\")]
	for _, want := range []string{"1", "2", "3", "4", "5", "7", "8", "9", "53"} {
		if !strings.Contains(params, want) {
			t.Errorf("missing attribute %q in %q", want, params)
		}
	}
}

// TestDECRQSS_DECSTBMDefault verifies a DECRQSS scroll-region query returns the
// full-screen default region.
func TestDECRQSS_DECSTBMDefault(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1bP$qr\x1b\\"))
	want := "\x1bP1$r1;24r\x1b\\"
	if got := string(s.Response); got != want {
		t.Errorf("DECRQSS DECSTBM default = %q, want %q", got, want)
	}
}

// TestDECRQSS_DECSTBMCustom verifies the scroll-region query reflects a custom
// region set via DECSTBM.
func TestDECRQSS_DECSTBMCustom(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1b[5;20r")) // set scroll region
	s.Write([]byte("\x1bP$qr\x1b\\"))
	want := "\x1bP1$r5;20r\x1b\\"
	if got := string(s.Response); got != want {
		t.Errorf("DECRQSS DECSTBM = %q, want %q", got, want)
	}
}

// TestDECRQSS_DECSCUSRStyles verifies the cursor-style query echoes each style
// value set via DECSCUSR.
func TestDECRQSS_DECSCUSRStyles(t *testing.T) {
	cases := []struct {
		name string
		set  string
		want string
	}{
		{"0 blinking block", "\x1b[0 q", "\x1bP1$r0 q\x1b\\"},
		{"1 blinking block", "\x1b[1 q", "\x1bP1$r1 q\x1b\\"},
		{"2 steady block", "\x1b[2 q", "\x1bP1$r2 q\x1b\\"},
		{"3 blinking underline", "\x1b[3 q", "\x1bP1$r3 q\x1b\\"},
		{"4 steady underline", "\x1b[4 q", "\x1bP1$r4 q\x1b\\"},
		{"5 blinking bar", "\x1b[5 q", "\x1bP1$r5 q\x1b\\"},
		{"6 steady bar", "\x1b[6 q", "\x1bP1$r6 q\x1b\\"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := New(24, 80)
			s.Write([]byte(tc.set))
			s.Write([]byte("\x1bP$q q\x1b\\"))
			if got := string(s.Response); got != tc.want {
				t.Errorf("DECRQSS DECSCUSR after %q = %q, want %q", tc.set, got, tc.want)
			}
		})
	}
}

// TestDECRQSS_UnsupportedQueries verifies every unrecognized or malformed query
// returns the invalid-status response (0$r).
func TestDECRQSS_UnsupportedQueries(t *testing.T) {
	bad := []string{"z", "Z", "\"p", "\"q", "$}", "!p", "mm", "rr", " qq", "123", "", strings.Repeat("x", 200)}
	want := "\x1bP0$r\x1b\\"
	for _, q := range bad {
		t.Run(q, func(t *testing.T) {
			s := New(24, 80)
			s.Write([]byte("\x1bP$q" + q + "\x1b\\"))
			if got := string(s.Response); got != want {
				t.Errorf("DECRQSS unsupported %q = %q, want %q", q, got, want)
			}
		})
	}
}

// TestDECRQSSBufferBoundedAtMax verifies the DECRQSS data buffer is capped at
// maxDCSLen, and an oversized (garbage) query still yields an invalid response
// and returns the parser to Ground after ST.
func TestDECRQSSBufferBoundedAtMax(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1bP$q"))
	data := make([]byte, maxDCSLen+10)
	for i := range data {
		data[i] = 'A'
	}
	s.Write(data)
	if len(s.dcsBuf) != maxDCSLen {
		t.Errorf("dcsBuf = %d, want %d (capped)", len(s.dcsBuf), maxDCSLen)
	}
	s.Write([]byte("\x1b\\"))
	if !strings.HasPrefix(string(s.Response), "\x1bP0$r") {
		t.Errorf("oversized DECRQSS response = %q, want invalid (0$r) prefix", s.Response)
	}
	if s.pState != stGround {
		t.Errorf("state after ST = %d, want Ground", s.pState)
	}
}

// TestUnknownDCSNotBuffered verifies an unknown DCS (unrecognized final byte)
// buffers no data and returns to Ground on ST.
func TestUnknownDCSNotBuffered(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1bPz")) // unknown DCS final 'z'
	s.Write([]byte("this should not be buffered at all"))
	if len(s.dcsBuf) != 0 {
		t.Errorf("unknown DCS buffered %d bytes, want 0", len(s.dcsBuf))
	}
	s.Write([]byte("\x1b\\"))
	if s.pState != stGround {
		t.Errorf("state after unknown DCS ST = %d, want Ground", s.pState)
	}
}

// TestDCS8BitSTTerminates verifies the 8-bit ST (0x9C) terminates a DECRQSS and
// produces a response.
func TestDCS8BitSTTerminates(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1bP$qm")) // DECRQSS for SGR
	s.Write([]byte{0x9C})       // 8-bit ST
	if s.pState != stGround {
		t.Errorf("8-bit ST did not terminate DCS: state=%d", s.pState)
	}
	if len(s.Response) == 0 {
		t.Error("DECRQSS with 8-bit ST produced no response")
	}
}

// TestMultipleSequentialDECRQSS verifies two DECRQSS queries against different
// state produce independent (different) responses.
func TestMultipleSequentialDECRQSS(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1bP$qm\x1b\\"))
	first := string(s.Response)
	s.Response = nil
	s.Write([]byte("\x1b[1m")) // set bold
	s.Write([]byte("\x1bP$qm\x1b\\"))
	second := string(s.Response)
	if first == second {
		t.Error("two DECRQSS with different state gave same response")
	}
}

// TestDCSAbortedByCAN verifies CAN aborts an in-progress DCS without dispatching
// (no response).
func TestDCSAbortedByCAN(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1bP$qm")) // DECRQSS for SGR
	s.Write([]byte{0x18})       // CAN
	if s.pState != stGround {
		t.Errorf("CAN did not abort DCS: state=%d", s.pState)
	}
	if len(s.Response) != 0 {
		t.Errorf("CAN in DCS produced response: %q", s.Response)
	}
}

// TestDCSAbortedBySUB verifies SUB aborts an in-progress DCS without dispatching.
func TestDCSAbortedBySUB(t *testing.T) {
	s := New(24, 80)
	s.Write([]byte("\x1bP$qm")) // DECRQSS for SGR
	s.Write([]byte{0x1A})       // SUB
	if s.pState != stGround {
		t.Errorf("SUB did not abort DCS: state=%d", s.pState)
	}
	if len(s.Response) != 0 {
		t.Errorf("SUB in DCS produced response: %q", s.Response)
	}
}
