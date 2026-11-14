package osc52copy_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/cplieger/web-terminal-engine/v6/osc52copy"
	"github.com/cplieger/web-terminal-engine/v6/vt"
)

// maxText is the longest text whose "52;c;<base64>" payload fits vt's OSC 52
// cap: base64 turns every 3 bytes into 4.
const maxText = (vt.MaxOSC52Len - len("52;c;")) / 4 * 3

// osc52 is the sequence xterm's ctlseqs documents for a clipboard write:
// OSC 52 ; c ; <base64> ST.
func osc52(text string) string {
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x1b\\"
}

type run struct {
	tty, stderr bytes.Buffer
	code        int
}

func runMain(argv0 string, args []string, stdin string) *run {
	r := &run{}
	r.code = osc52copy.Main(argv0, args, osc52copy.Streams{
		Stdin:  strings.NewReader(stdin),
		TTY:    &r.tty,
		Stderr: &r.stderr,
	})
	return r
}

func TestMainCopiesForEachNameAndFlagSet(t *testing.T) {
	cases := []struct {
		name  string
		argv0 string
		args  []string
	}{
		{"xclip_selection_clipboard", "xclip", []string{"-selection", "clipboard"}},
		{"xclip_selection_clipboard_in", "xclip", []string{"-selection", "clipboard", "-i"}},
		{"xclip_in_selection_clipboard", "xclip", []string{"-i", "-selection", "clipboard"}},
		{"xclip_sel_clip", "xclip", []string{"-sel", "clip"}},
		{"xclip_by_path", "/usr/local/bin/xclip", []string{"-selection", "clipboard"}},
		{"xsel_clipboard_input", "xsel", []string{"--clipboard", "--input"}},
		{"xsel_input_clipboard", "xsel", []string{"--input", "--clipboard"}},
		{"xsel_b_i", "xsel", []string{"-b", "-i"}},
		{"xsel_clipboard", "xsel", []string{"--clipboard"}},
		{"xclip_selection_by_first_letter", "xclip", []string{"-selection", "c"}},
		{"xclip_selection_upper_case", "xclip", []string{"-selection", "CLIPBOARD"}},
		{"xclip_quiet_in_selection", "xclip", []string{"-quiet", "-i", "-selection", "clipboard"}},
		{"xclip_silent_verbose", "xclip", []string{"-silent", "-verbose", "-sel", "clip"}},
		{"xclip_target_utf8_string", "xclip", []string{"-selection", "clipboard", "-target", "UTF8_STRING"}},
		{"xclip_t_text_plain", "xclip", []string{"-selection", "clipboard", "-t", "text/plain"}},
		{"xclip_t_text_plain_utf8", "xclip", []string{"-sel", "c", "-t", "text/plain;charset=utf-8"}},
		{"xclip_target_string", "xclip", []string{"-selection", "clipboard", "-target", "STRING"}},
		{"xclip_target_text", "xclip", []string{"-selection", "clipboard", "-target", "TEXT"}},
		{"xclip_unique_prefixes", "xclip", []string{"-q", "-selec", "clipboard", "-ta", "TEXT"}},
		{"xsel_nodetach_input_clipboard", "xsel", []string{"--nodetach", "-i", "-b"}},
		{"xsel_verbose", "xsel", []string{"--verbose", "--clipboard", "--input"}},
		{"xsel_combined_bi", "xsel", []string{"-bi"}},
		{"xsel_combined_ib", "xsel", []string{"-ib"}},
		{"xsel_combined_nib_v", "xsel", []string{"-nib", "-v"}},
		{"wl_copy", "wl-copy", nil},
		{"wl_copy_foreground", "wl-copy", []string{"--foreground"}},
		{"wl_copy_f", "wl-copy", []string{"-f"}},
		{"wl_copy_type_text_plain", "wl-copy", []string{"--type", "text/plain"}},
		{"wl_copy_type_equals", "wl-copy", []string{"--type=text/plain;charset=utf-8"}},
		{"wl_copy_t_f", "wl-copy", []string{"-t", "text/plain", "-f"}},
		{"wl_copy_t_sticky", "wl-copy", []string{"-ttext/plain"}},
	}
	text := "copy \u2713 me\nline two\n"
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := runMain(c.argv0, c.args, text)
			if r.code != 0 {
				t.Errorf("exit = %d, want 0 (stderr %q)", r.code, r.stderr.String())
			}
			if got, want := r.tty.String(), osc52(text); got != want {
				t.Errorf("tty got %q, want %q", got, want)
			}
			if r.stderr.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", r.stderr.String())
			}
		})
	}
}

func TestMainEmptyInputWritesAnEmptySet(t *testing.T) {
	r := runMain("wl-copy", nil, "")
	if r.code != 0 {
		t.Errorf("exit = %d, want 0 (stderr %q)", r.code, r.stderr.String())
	}
	if got, want := r.tty.String(), "\x1b]52;c;\x1b\\"; got != want {
		t.Errorf("tty got %q, want %q", got, want)
	}
}

func TestMainCopyAtTheCapReachesTheScreen(t *testing.T) {
	text := strings.Repeat("abc", maxText/3)
	if len(text) != maxText {
		t.Fatalf("fixture is %d bytes, want %d", len(text), maxText)
	}
	r := runMain("xclip", []string{"-selection", "clipboard"}, text)
	if r.code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", r.code, r.stderr.String())
	}
	s := vt.New(24, 80)
	s.Write(r.tty.Bytes())
	if got := s.TakeClipboard(); string(got) != text {
		t.Errorf("the screen queued %d bytes from the stand-in's sequence, want the %d-byte text", len(got), len(text))
	}
	if s.TakeClipboardRefused() {
		t.Error("the screen refused the stand-in's at-cap sequence")
	}
}

func TestMainRefusesOversizeInput(t *testing.T) {
	for _, argv0 := range []string{"xclip", "xsel", "wl-copy"} {
		t.Run(argv0, func(t *testing.T) {
			args := map[string][]string{"xclip": {"-selection", "clipboard"}, "xsel": {"--clipboard", "--input"}}[argv0]
			r := runMain(argv0, args, strings.Repeat("z", maxText+1))
			if r.code == 0 {
				t.Error("exit = 0, want non-zero for an oversize copy")
			}
			if r.tty.Len() != 0 {
				t.Errorf("tty got %d bytes, want nothing written", r.tty.Len())
			}
			if got := r.stderr.String(); !strings.HasPrefix(got, argv0+": copy too large to send to the browser") || !strings.HasSuffix(got, "\n") {
				t.Errorf("stderr = %q, want one line starting %q", got, argv0+": copy too large to send to the browser")
			}
		})
	}
}

func TestMainRefusesPaste(t *testing.T) {
	cases := []struct {
		name  string
		argv0 string
		args  []string
	}{
		{"xclip_o", "xclip", []string{"-o"}},
		{"xclip_selection_clipboard_o", "xclip", []string{"-selection", "clipboard", "-o"}},
		{"xclip_out", "xclip", []string{"-out", "-sel", "clip"}},
		{"xclip_ou_prefix", "xclip", []string{"-selection", "c", "-ou"}},
		{"xsel_combined_bo", "xsel", []string{"-bo"}},
		{"xsel_o", "xsel", []string{"-o"}},
		{"xsel_clipboard_output", "xsel", []string{"--clipboard", "--output"}},
		{"xsel_b_o", "xsel", []string{"-b", "-o"}},
		{"wl_paste", "wl-paste", nil},
		{"wl_paste_no_newline", "wl-paste", []string{"-n"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := runMain(c.argv0, c.args, "ignored")
			if r.code == 0 {
				t.Error("exit = 0, want non-zero for a paste")
			}
			if r.tty.Len() != 0 {
				t.Errorf("tty got %q, want nothing written", r.tty.String())
			}
			if got, want := r.stderr.String(), c.argv0+": paste is not supported in this terminal\n"; got != want {
				t.Errorf("stderr = %q, want %q", got, want)
			}
		})
	}
}

func TestMainFailsClosedOnOtherArguments(t *testing.T) {
	cases := []struct {
		name  string
		argv0 string
		args  []string
	}{
		{"xclip_bare_means_primary", "xclip", nil},
		{"xclip_selection_primary", "xclip", []string{"-selection", "primary"}},
		{"xclip_selection_missing_value", "xclip", []string{"-selection"}},
		{"xclip_selection_secondary", "xclip", []string{"-selection", "s"}},
		{"xclip_selection_buffer_cut", "xclip", []string{"-selection", "buffer-cut"}},
		{"xclip_selection_empty", "xclip", []string{"-selection", ""}},
		{"xclip_target", "xclip", []string{"-selection", "clipboard", "-t", "image/png"}},
		{"xclip_target_missing_value", "xclip", []string{"-selection", "clipboard", "-target"}},
		{"xclip_rmlastnl", "xclip", []string{"-selection", "clipboard", "-rmlastnl"}},
		{"xclip_filter", "xclip", []string{"-selection", "clipboard", "-filter"}},
		{"xclip_ambiguous_prefix", "xclip", []string{"-se", "clipboard"}},
		{"xclip_file", "xclip", []string{"-selection", "clipboard", "notes.txt"}},
		{"xsel_bare_means_primary", "xsel", nil},
		{"xsel_input_only", "xsel", []string{"--input"}},
		{"xsel_primary", "xsel", []string{"--primary", "--input"}},
		{"xsel_clear", "xsel", []string{"--clipboard", "--clear"}},
		{"xsel_append", "xsel", []string{"-ba"}},
		{"xsel_combined_primary", "xsel", []string{"-bpi"}},
		{"wl_copy_text_argument", "wl-copy", []string{"hello"}},
		{"wl_copy_primary", "wl-copy", []string{"--primary"}},
		{"wl_copy_p", "wl-copy", []string{"-p"}},
		{"wl_copy_trim_newline", "wl-copy", []string{"-n"}},
		{"wl_copy_paste_once", "wl-copy", []string{"-o"}},
		{"wl_copy_clear", "wl-copy", []string{"--clear"}},
		{"wl_copy_type_image", "wl-copy", []string{"--type", "image/png"}},
		{"wl_copy_type_missing_value", "wl-copy", []string{"-t"}},
		{"wl_copy_type_equals_html", "wl-copy", []string{"--type=text/html"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := runMain(c.argv0, c.args, "text")
			if r.code == 0 {
				t.Error("exit = 0, want non-zero for unsupported arguments")
			}
			if r.tty.Len() != 0 {
				t.Errorf("tty got %q, want nothing written", r.tty.String())
			}
			if got := r.stderr.String(); !strings.HasPrefix(got, c.argv0+": ") || strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "\n") {
				t.Errorf("stderr = %q, want one line starting %q", got, c.argv0+": ")
			}
		})
	}
}

func TestMainRefusesAnUnknownName(t *testing.T) {
	r := runMain("pbcopy", nil, "text")
	if r.code == 0 {
		t.Error("exit = 0, want non-zero for a name this package does not stand in for")
	}
	if r.tty.Len() != 0 {
		t.Errorf("tty got %q, want nothing written", r.tty.String())
	}
	if r.stderr.Len() == 0 {
		t.Error("stderr is empty, want the reason")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("no terminal") }

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestMainReportsIOFailures(t *testing.T) {
	t.Run("tty_write", func(t *testing.T) {
		var stderr bytes.Buffer
		code := osc52copy.Main("wl-copy", nil, osc52copy.Streams{Stdin: strings.NewReader("x"), TTY: failingWriter{}, Stderr: &stderr})
		if code == 0 {
			t.Error("exit = 0, want non-zero when the terminal write fails")
		}
		if !strings.Contains(stderr.String(), "no terminal") {
			t.Errorf("stderr = %q, want the write error", stderr.String())
		}
	})
	t.Run("stdin_read", func(t *testing.T) {
		var tty, stderr bytes.Buffer
		code := osc52copy.Main("wl-copy", nil, osc52copy.Streams{Stdin: failingReader{}, TTY: &tty, Stderr: &stderr})
		if code == 0 {
			t.Error("exit = 0, want non-zero when stdin fails")
		}
		if tty.Len() != 0 {
			t.Errorf("tty got %q, want nothing written", tty.String())
		}
		if !strings.Contains(stderr.String(), "broken pipe") {
			t.Errorf("stderr = %q, want the read error", stderr.String())
		}
	})
}

func TestNames(t *testing.T) {
	names := osc52copy.Names()
	want := []string{"wl-copy", "wl-paste", "xclip", "xsel"}
	got := slices.Sorted(slices.Values(names))
	if !slices.Equal(got, want) {
		t.Errorf("Names() = %q, want %q", got, want)
	}
	names[0] = "changed"
	if again := slices.Sorted(slices.Values(osc52copy.Names())); !slices.Equal(again, want) {
		t.Errorf("Names() after a caller edit = %q, want %q", again, want)
	}
}
