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

func (l *Lines) Join(sep string) string {
	return strings.Join(l.l, sep)
}

func (l *Lines) String() string {
	return l.Join("\n")
}

func SliceToLines[T fmt.Stringer](slice []T, prefix ...string) *Lines {
	ret := New(prefix...)
	for i := range slice {
		ret.Add(slice[i].String())
	}
	return ret
}
