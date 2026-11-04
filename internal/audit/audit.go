package audit

import (
	"context"
	"strings"
	"time"
	"unicode"
)

type Event struct {
	Time                                                                                 time.Time
	Type, SubjectID, CredentialID, MachineID, ActionID, Result, RequestID, SourceAddress string
	detail                                                                               string
}

func (e *Event) SetDetail(s string) {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	r := []rune(s)
	if len(r) > 256 {
		r = r[:256]
	}
	e.detail = string(r)
}
func (e Event) Detail() string { return e.detail }

type Writer interface {
	WriteEvent(context.Context, Event) error
}
