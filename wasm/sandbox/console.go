//go:build js

package sandbox

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"syscall/js"
)

// Console is an io.WriteCloser that writes lines to the browser's console,
// with the colours the pretty log exporter put into them.
//
// `wasm_exec.js` already sends Go's stderr to `console.log`, but as text:
// the escape sequences the exporter writes for a terminal arrive as `[36m`
// in the middle of the line, because a browser console does not read ANSI.
// What it reads is `%c` followed by a CSS string per styled run, so this
// turns the one into the other and calls `console.log` itself. The exporter
// is not told it is not on a terminal -- `color.NoColor` is forced off in
// `main` -- because the whole point is that it colours.
//
// Line-buffered, since a record is a line and the console draws a call at a
// time.
type Console struct {
	buf bytes.Buffer
}

// NewConsole is a [Console] as the exporter opens one.
func NewConsole() (io.WriteCloser, error) { return &Console{}, nil }

func (c *Console) Write(p []byte) (int, error) {
	c.buf.Write(p)
	for {
		line, rest, ok := bytes.Cut(c.buf.Bytes(), []byte{'\n'})
		if !ok {
			return len(p), nil
		}
		emit(string(line))
		c.buf.Next(len(line) + 1)
		_ = rest
	}
}

func (c *Console) Close() error {
	if c.buf.Len() > 0 {
		emit(c.buf.String())
		c.buf.Reset()
	}

	return nil
}

// emit writes one line: the text with `%c` at every change of style, and
// the styles as the arguments that follow.
func emit(line string) {
	format, styles := restyle(line)
	args := make([]any, 0, len(styles)+1)
	args = append(args, format)
	for _, s := range styles {
		args = append(args, s)
	}
	js.Global().Get("console").Call("log", args...)
}

// restyle reads the SGR sequences (`ESC [ … m`) out of a line and answers the
// text with `%c` where each one was, and the CSS each one means from then on.
// Everything else escape-shaped is dropped.
func restyle(line string) (string, []string) {
	var out strings.Builder
	var styles []string
	st := style{}
	for i := 0; i < len(line); i++ {
		if line[i] != 0x1b {
			if line[i] == '%' {
				out.WriteString("%%")
			} else {
				out.WriteByte(line[i])
			}

			continue
		}
		// ESC [ params m
		if i+1 >= len(line) || line[i+1] != '[' {
			continue
		}
		j := i + 2
		for j < len(line) && (line[j] >= '0' && line[j] <= '9' || line[j] == ';') {
			j++
		}
		if j >= len(line) {
			break
		}
		if line[j] == 'm' {
			st.apply(strings.Split(line[i+2:j], ";"))
			out.WriteString("%c")
			styles = append(styles, st.css())
		}
		i = j
	}

	return out.String(), styles
}

// style is what SGR has said so far.
type style struct {
	bold, faint, underline bool
	fg                     string
}

func (s *style) apply(params []string) {
	for i := 0; i < len(params); i++ {
		n, _ := strconv.Atoi(params[i])
		switch {
		case params[i] == "" || n == 0:
			*s = style{}
		case n == 1:
			s.bold = true
		case n == 2:
			s.faint = true
		case n == 4:
			s.underline = true
		case n == 22:
			s.bold, s.faint = false, false
		case n == 24:
			s.underline = false
		case n == 39:
			s.fg = ""
		case n >= 30 && n <= 37:
			s.fg = ansi[n-30]
		case n >= 90 && n <= 97:
			s.fg = ansiBright[n-90]
		case n == 38 && i+4 < len(params) && params[i+1] == "2":
			r, _ := strconv.Atoi(params[i+2])
			g, _ := strconv.Atoi(params[i+3])
			b, _ := strconv.Atoi(params[i+4])
			s.fg = fmt.Sprintf("rgb(%d,%d,%d)", r, g, b)
			i += 4
		case n == 38 && i+2 < len(params) && params[i+1] == "5":
			// 256-colour: the sixteen named ones, and grey for the rest.
			c, _ := strconv.Atoi(params[i+2])
			switch {
			case c < 8:
				s.fg = ansi[c]
			case c < 16:
				s.fg = ansiBright[c-8]
			default:
				s.fg = "gray"
			}
			i += 2
		}
	}
}

func (s style) css() string {
	var b strings.Builder
	if s.fg != "" {
		b.WriteString("color:" + s.fg + ";")
	}
	if s.bold {
		b.WriteString("font-weight:bold;")
	}
	if s.faint {
		b.WriteString("opacity:0.6;")
	}
	if s.underline {
		b.WriteString("text-decoration:underline;")
	}

	return b.String()
}

// The sixteen, as a dark console shows them; a light one reads them too.
var (
	ansi       = [8]string{"#3b3b3b", "#c0392b", "#2f855a", "#b58900", "#3b5bdb", "#8e44ad", "#0e7c86", "#8a8a8a"}
	ansiBright = [8]string{"#6b6b6b", "#f07167", "#5cb885", "#e0b341", "#7b93ff", "#c084fc", "#22b8cf", "#e6e8eb"}
)
