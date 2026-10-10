package util

import "io"

// LinePrefixWriter prefixes every line written through it, so the output of
// a process running inside another one is told apart from the host's own.
// A line is taken to end at '\n'; a write that ends mid-line is continued by
// the next write without a prefix.
type LinePrefixWriter struct {
	w      io.Writer
	prefix []byte
	atBOL  bool
}

func NewLinePrefixWriter(w io.Writer, prefix string) *LinePrefixWriter {
	return &LinePrefixWriter{w: w, prefix: []byte(prefix), atBOL: true}
}

func (p *LinePrefixWriter) Write(b []byte) (int, error) {
	out := make([]byte, 0, len(b)+len(p.prefix)*4)
	for _, c := range b {
		if p.atBOL {
			out = append(out, p.prefix...)
			p.atBOL = false
		}
		out = append(out, c)
		if c == '\n' {
			p.atBOL = true
		}
	}
	if _, err := p.w.Write(out); err != nil {
		return 0, err
	}
	return len(b), nil
}
