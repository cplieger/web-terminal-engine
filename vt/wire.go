package vt

import "strings"

// WireRun is a contiguous run of cells with the same style.
// Text is the run's content; FG/BG are 0xRRGGBB or -1 for default.
// Attr is a bit mask: 1=bold, 2=italic, 4=underline, 8=inverse,
// 16=strikethrough, 32=dim, 64=hidden, 128=blink, 256=overline,
// 512=double-underline.
type WireRun struct {
	T  string `json:"t"`
	F  int32  `json:"f,omitempty"`
	B  int32  `json:"b,omitempty"`
	Uc int32  `json:"uc,omitempty"`
	A  uint16 `json:"a,omitempty"`
}

// Default flag for FG/BG meaning "use theme default".
const wireDefaultColor = int32(-1)

// RenderRowWire returns a row as a slice of style runs for the canvas
// renderer. Same-style consecutive cells are coalesced into a single run.
func (s *Screen) RenderRowWire(y int) []WireRun {
	if y < 0 || y >= s.Height {
		return nil
	}
	return cellsToRuns(s.Cells[y])
}

// cellsToRuns converts a row of cells to wire runs (same-style coalesced).
func cellsToRuns(row []Cell) []WireRun {
	var runs []WireRun
	if len(row) == 0 {
		return runs
	}
	var buf strings.Builder
	prev := row[0].Style
	for x, cell := range row {
		if x > 0 && cell.Style != prev {
			runs = append(runs, makeRun(buf.String(), prev))
			buf.Reset()
			prev = cell.Style
		}
		ch := cell.Ch
		if ch == 0 {
			ch = '\uFFFF'
		}
		buf.WriteRune(ch)
	}
	if buf.Len() > 0 {
		runs = append(runs, makeRun(buf.String(), prev))
	}
	return runs
}

func makeRun(text string, st Style) WireRun {
	fg, bg := st.FG, st.BG
	if st.Inverse {
		fg, bg = bg, fg
	}
	r := WireRun{T: text}
	r.F = colorToWire(fg)
	r.B = colorToWire(bg)
	r.Uc = colorToWire(st.UnderlineColor)
	if st.Bold {
		r.A |= 1
	}
	if st.Italic {
		r.A |= 2
	}
	if st.Underline {
		r.A |= 4
	}
	if st.Inverse {
		r.A |= 8
	}
	if st.Strikethrough {
		r.A |= 16
	}
	if st.Dim {
		r.A |= 32
	}
	if st.Hidden {
		r.A |= 64
	}
	if st.Blink {
		r.A |= 128
	}
	if st.Overline {
		r.A |= 256
	}
	if st.DoubleUnderline {
		r.A |= 512
	}
	return r
}

func colorToWire(c Color) int32 {
	switch c.Type {
	case 0:
		return wireDefaultColor
	case 1:
		// Basic 8/16 — match the same palette as html.go's css classes.
		return basic16RGB(c.Val)
	case 2:
		return color256RGB(c.Val)
	case 3:
		return int32(c.R)<<16 | int32(c.G)<<8 | int32(c.B)
	}
	return wireDefaultColor
}

func basic16RGB(idx uint8) int32 {
	pal := [16]int32{
		0x000000, 0xaa0000, 0x00aa00, 0xaa5500,
		0x0000aa, 0xaa00aa, 0x00aaaa, 0xaaaaaa,
		0x555555, 0xff5555, 0x55ff55, 0xffff55,
		0x5555ff, 0xff55ff, 0x55ffff, 0xffffff,
	}
	if int(idx) < len(pal) {
		return pal[idx]
	}
	return 0xaaaaaa
}

func color256RGB(idx uint8) int32 {
	if idx < 16 {
		return basic16RGB(idx)
	}
	if idx < 232 {
		i := idx - 16
		b := i % 6
		g := (i / 6) % 6
		r := i / 36
		toVal := func(v uint8) int32 {
			if v == 0 {
				return 0
			}
			return int32(55 + int(v)*40) // #nosec G115 -- bounded palette value
		}
		return toVal(r)<<16 | toVal(g)<<8 | toVal(b)
	}
	v := int32(8 + int(idx-232)*10) // #nosec G115 -- bounded grayscale ramp
	return v<<16 | v<<8 | v
}
