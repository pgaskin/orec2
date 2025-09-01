package schema

import (
	"strings"
	"time"
)

//go:generate go run github.com/bufbuild/buf/cmd/buf@v1.57.0 generate --template {"version":"v2","plugins":[{"local":["go","tool","protoc-gen-go"],"out":".","opt":["paths=source_relative","Mschema.proto=./schema"]}]}

func ToWeekday(w time.Weekday) Weekday {
	return Weekday(w)
}

func (w Weekday) AsWeekday() time.Weekday {
	return time.Weekday(w)
}

type ClockTime int32

func MakeClockTime(hh, mm int) ClockTime {
	if hh < 0 || mm < 0 {
		return -1
	}
	return ClockTime(hh*60 + mm)
}

func (t ClockTime) IsValid() bool {
	return t >= 0
}

func (t ClockTime) Split() (d int, hh, mm int) {
	if t >= 0 {
		d = int(t / (24 * 60))
		t %= 24 * 60
		hh = int(t / 60)
		mm = int(t % 60)
	}
	return
}

func (t ClockTime) String() string {
	return t.Format(true)
}

func (t ClockTime) Format(ampm bool) string {
	if !t.IsValid() {
		return "invalid"
	}
	var b strings.Builder
	d, hh, mm := t.Split()
	ap := byte('a')
	if ampm && hh >= 12 {
		ap = 'p'
		hh -= 12
	}
	for range d {
		b.WriteByte('>')
	}
	if ampm && hh == 0 {
		b.WriteByte('1')
		b.WriteByte('2')
	} else {
		if !ampm || hh >= 10 {
			b.WriteByte('0' + byte(hh/10))
		}
		b.WriteByte('0' + byte(hh%10))
	}
	b.WriteByte(':')
	b.WriteByte('0' + byte(mm/10))
	b.WriteByte('0' + byte(mm%10))
	if ampm {
		b.WriteByte(ap)
		b.WriteByte('m')
	}
	return b.String()
}

func (t ClockTime) Norm() ClockTime {
	if t < 0 {
		t = -1
	}
	return t
}

type ClockRange struct {
	Start ClockTime
	End   ClockTime
}

func MakeClockRange(hh1, mm1, hh2, mm2 int) ClockRange {
	r := ClockRange{
		Start: MakeClockTime(hh1, mm1),
		End:   MakeClockTime(hh2, mm2),
	}
	if r.End < r.Start {
		r.End += 24 * 60
	}
	return r
}

func (r ClockRange) IsValid() bool {
	return r.Start.IsValid() && r.End.IsValid() && r.Start < r.End
}

func (r ClockRange) String() string {
	return r.Format(true)
}

func (r ClockRange) Format(ampm bool) string {
	if !r.IsValid() {
		return "invalid"
	}
	x := r.Start.Format(ampm)
	y := r.End.Format(ampm)
	if r.End-r.Start < 24*60 && r.Start < 24*60 {
		if y[0] == '>' {
			y = y[1:]
		}
		if ampm && x[len(x)-2] == y[len(y)-2] {
			x = x[:len(x)-2]
		}
	}
	return x + " - " + y
}

func (r ClockRange) Overlaps(o ClockRange) bool {
	return r.IsValid() && r.Start <= o.End && o.Start <= r.End
}
