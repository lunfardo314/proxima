package lines

import (
	"fmt"
	"strings"

	"github.com/lunfardo314/proxima/util/lazyargs"
)

type Lines struct {
	prefix string
	l      []string
	dummy  bool
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

func NewDummy() *Lines {
	return &Lines{dummy: true}
}

func (l *Lines) Add(format string, args ...any) *Lines {
	if l.dummy {
		return l
	}
	l.l = append(l.l, fmt.Sprintf(l.prefix+format, args...))
	return l
}

func (l *Lines) Trace(format string, lazyArgs ...any) *Lines {
	if l.dummy {
		return l
	}
	return l.Add(format, lazyargs.Eval(lazyArgs...)...)
}

func (l *Lines) Append(ln *Lines) *Lines {
	if l.dummy {
		return l
	}
	l.l = append(l.l, ln.l...)
	return l
}

func (l *Lines) Join(sep string) string {
	if l.dummy {
		return ""
	}
	return strings.Join(l.l, sep)
}

func (l *Lines) EOL() string {
	return l.Join("\n")
}

func (l *Lines) String() string {
	if l.dummy {
		return ""
	}
	return l.Join("\n")
}

func (l *Lines) Slice() []string {
	return l.l
}

func SliceToLines[T fmt.Stringer](slice []T, prefix ...string) *Lines {
	ret := New(prefix...)
	for i := range slice {
		ret.Add(slice[i].String())
	}
	return ret
}
