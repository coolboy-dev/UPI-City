// Package plot renders the report figures as dependency-free SVG.
//
// Written in Go rather than with matplotlib on purpose. The alternative meant
// ~200 MB of scientific-Python dependencies for three chart types, on a
// machine with little free memory, and a second toolchain in the demo path.
// SVG also embeds directly in the README and, later, in the dashboard, and it
// renders identically everywhere — which matters when the figures are the
// evidence.
package plot

import (
	"fmt"
	"html"
	"math"
	"strings"
)

// Canvas is a minimal SVG scene with a linear-mapped plot area.
type Canvas struct {
	W, H       int
	L, R, T, B int // margins
	XMin, XMax float64
	YMin, YMax float64
	Title      string
	XLabel     string
	YLabel     string

	// grid is rendered before body so axes sit behind the data. Kept as its
	// own builder rather than splicing the two, because strings.Builder
	// cannot legally be copied by value once written to.
	grid strings.Builder
	body strings.Builder
}

// New returns a canvas with sensible margins.
func New(w, h int, title, xlabel, ylabel string) *Canvas {
	return &Canvas{
		W: w, H: h, L: 64, R: 24, T: 44, B: 52,
		XMin: 0, XMax: 1, YMin: 0, YMax: 1,
		Title: title, XLabel: xlabel, YLabel: ylabel,
	}
}

func (c *Canvas) px(x float64) float64 {
	if c.XMax == c.XMin {
		return float64(c.L)
	}
	f := (x - c.XMin) / (c.XMax - c.XMin)
	return float64(c.L) + f*float64(c.W-c.L-c.R)
}

func (c *Canvas) py(y float64) float64 {
	if c.YMax == c.YMin {
		return float64(c.H - c.B)
	}
	f := (y - c.YMin) / (c.YMax - c.YMin)
	return float64(c.H-c.B) - f*float64(c.H-c.T-c.B)
}

// Polyline draws a connected series.
func (c *Canvas) Polyline(xs, ys []float64, colour string, width float64, dash string) {
	if len(xs) == 0 {
		return
	}
	var sb strings.Builder
	for i := range xs {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "%.2f,%.2f", c.px(xs[i]), c.py(ys[i]))
	}
	d := ""
	if dash != "" {
		d = fmt.Sprintf(` stroke-dasharray="%s"`, dash)
	}
	fmt.Fprintf(&c.body,
		`<polyline points="%s" fill="none" stroke="%s" stroke-width="%.1f"%s stroke-linejoin="round"/>`+"\n",
		sb.String(), colour, width, d)
}

// Marker draws a labelled point.
func (c *Canvas) Marker(x, y float64, colour, label string) {
	fmt.Fprintf(&c.body,
		`<circle cx="%.2f" cy="%.2f" r="4.5" fill="%s" stroke="var(--bg)" stroke-width="1.5"/>`+"\n",
		c.px(x), c.py(y), colour)
	if label != "" {
		fmt.Fprintf(&c.body,
			`<text x="%.2f" y="%.2f" font-size="11" fill="%s">%s</text>`+"\n",
			c.px(x)+8, c.py(y)-7, colour, html.EscapeString(label))
	}
}

// HBar draws a horizontal bar with a label and a value.
func (c *Canvas) HBar(row int, rows int, frac float64, colour, label, value string) {
	area := float64(c.H - c.T - c.B)
	h := area / float64(rows)
	y := float64(c.T) + float64(row)*h
	bh := h * 0.58
	w := frac * float64(c.W-c.L-c.R)
	if w < 1 && frac > 0 {
		w = 1
	}
	fmt.Fprintf(&c.body,
		`<rect x="%d" y="%.2f" width="%.2f" height="%.2f" fill="%s" rx="2"/>`+"\n",
		c.L, y+(h-bh)/2, w, bh, colour)
	fmt.Fprintf(&c.body,
		`<text x="%d" y="%.2f" font-size="11" text-anchor="end" fill="var(--fg)">%s</text>`+"\n",
		c.L-8, y+h/2+4, html.EscapeString(label))
	fmt.Fprintf(&c.body,
		`<text x="%.2f" y="%.2f" font-size="11" fill="var(--muted)">%s</text>`+"\n",
		float64(c.L)+w+6, y+h/2+4, html.EscapeString(value))
}

// Legend draws entries in the top-right of the plot area.
func (c *Canvas) Legend(entries []LegendEntry) {
	x := float64(c.W-c.R) - 150
	y := float64(c.T) + 10
	for i, e := range entries {
		yy := y + float64(i)*16
		fmt.Fprintf(&c.body,
			`<line x1="%.2f" y1="%.2f" x2="%.2f" y2="%.2f" stroke="%s" stroke-width="2.5"%s/>`+"\n",
			x, yy, x+18, yy, e.Colour, dashAttr(e.Dash))
		fmt.Fprintf(&c.body,
			`<text x="%.2f" y="%.2f" font-size="11" fill="var(--fg)">%s</text>`+"\n",
			x+24, yy+4, html.EscapeString(e.Label))
	}
}

// LegendEntry is one legend row.
type LegendEntry struct {
	Label  string
	Colour string
	Dash   string
}

func dashAttr(d string) string {
	if d == "" {
		return ""
	}
	return fmt.Sprintf(` stroke-dasharray="%s"`, d)
}

// Axes draws the frame, ticks and labels.
func (c *Canvas) Axes(xticks, yticks int) {
	b := &c.grid

	// Grid and ticks.
	for i := 0; i <= yticks; i++ {
		v := c.YMin + (c.YMax-c.YMin)*float64(i)/float64(yticks)
		y := c.py(v)
		fmt.Fprintf(b, `<line x1="%d" y1="%.2f" x2="%d" y2="%.2f" stroke="var(--grid)" stroke-width="1"/>`+"\n",
			c.L, y, c.W-c.R, y)
		fmt.Fprintf(b, `<text x="%d" y="%.2f" font-size="10" text-anchor="end" fill="var(--muted)">%s</text>`+"\n",
			c.L-6, y+3, trim(v))
	}
	for i := 0; i <= xticks; i++ {
		v := c.XMin + (c.XMax-c.XMin)*float64(i)/float64(xticks)
		x := c.px(v)
		fmt.Fprintf(b, `<line x1="%.2f" y1="%d" x2="%.2f" y2="%d" stroke="var(--grid)" stroke-width="1"/>`+"\n",
			x, c.T, x, c.H-c.B)
		fmt.Fprintf(b, `<text x="%.2f" y="%d" font-size="10" text-anchor="middle" fill="var(--muted)">%s</text>`+"\n",
			x, c.H-c.B+16, trim(v))
	}

	fmt.Fprintf(b, `<rect x="%d" y="%d" width="%d" height="%d" fill="none" stroke="var(--axis)" stroke-width="1"/>`+"\n",
		c.L, c.T, c.W-c.L-c.R, c.H-c.T-c.B)
}

func trim(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e6 {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.2f", v)
}

// String renders the finished SVG.
//
// Colours come from CSS variables with a media-query fallback, so a figure
// dropped into a light or dark page stays readable without regenerating it.
func (c *Canvas) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" font-family="ui-sans-serif,system-ui,sans-serif">`+"\n", c.W, c.H, c.W, c.H)
	b.WriteString(`<style>
svg{--bg:#ffffff;--fg:#1a1a1a;--muted:#666;--grid:#e8e8e8;--axis:#bbb}
@media (prefers-color-scheme:dark){svg{--bg:#111;--fg:#e8e8e8;--muted:#999;--grid:#2a2a2a;--axis:#444}}
</style>` + "\n")
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="var(--bg)"/>`+"\n", c.W, c.H)
	if c.Title != "" {
		fmt.Fprintf(&b, `<text x="%d" y="26" font-size="14" font-weight="600" fill="var(--fg)">%s</text>`+"\n",
			c.L, html.EscapeString(c.Title))
	}
	b.WriteString(c.grid.String()) // axes first, so data draws on top
	b.WriteString(c.body.String())
	if c.XLabel != "" {
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" font-size="11" text-anchor="middle" fill="var(--muted)">%s</text>`+"\n",
			float64(c.L+c.W-c.R)/2, c.H-10, html.EscapeString(c.XLabel))
	}
	if c.YLabel != "" {
		fmt.Fprintf(&b, `<text transform="translate(16,%.1f) rotate(-90)" font-size="11" text-anchor="middle" fill="var(--muted)">%s</text>`+"\n",
			float64(c.T+c.H-c.B)/2, html.EscapeString(c.YLabel))
	}
	b.WriteString("</svg>\n")
	return b.String()
}
