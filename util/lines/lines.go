package lines

import (
	"fmt"
	"strings"
)

type Lines struct {
	prefix string
	l      []string
}

func New(prefix ...string) *Lines {
	pref := ""
	if len(prefix) > 0 {
		pref = prefix[0]
	}
	return &Lines{
		prefix: pref,
		l:      make([]string, 0),
	}
}

func (l *Lines) Add(format string, args ...any) *Lines {
	l.l = append(l.l, fmt.Sprintf(l.prefix+format, args...))
	return l
}

func (l *Lines) Append(ln *Lines) *Lines {
	l.l = append(l.l, ln.l...)
	return l
}

func (l *Lines) String() string {
	return strings.Join(l.l, "\n")
}
