package main

import (
	"cmp"
	"strings"
	"testing"

	"github.com/pgaskin/orec2/schema"
)

func TestNormalizeText(t *testing.T) {
	for _, tc := range []struct {
		A, B string
		N, L bool
	}{
		{"", "", true, false},
		{"test\ntest", "test\ntest", true, false},
		{"  test\n  \u00a0\u00a0test\u2013  ", "test\n test-", true, false},
		{"  test\n  \u00a0\u00a0test\u2013  ", "test test-", false, false},
		{"  SDFsk jdnfks   jwERMwe   rkjwn   ", "sdfsk jdnfks jwermwe rkjwn", false, true},
		// TODO: more tests
	} {
		if c := normalizeText(tc.A, tc.N, tc.L); c != tc.B {
			t.Errorf("normalize %q (lower=%t): expected %q, got %q", tc.A, tc.L, tc.B, c)
		}
	}
}

func TestParseClockRange(t *testing.T) {
	for _, tc := range []struct {
		A, B string
	}{
		// invalid
		{"", ""},                  // empty
		{"1:00am", ""},            // TODO: should we parse this as a zero-length range?
		{"1:00pm", ""},            // TODO: same
		{"noon-noon", ""},         // two-component zero length range
		{"01:00-01:00", ""},       // two-component zero length range
		{"123-456", ""},           // invalid hour range
		{"1h00am-2h00pm", ""},     // french time with am/pm
		{"001:00-2:00", ""},       // hour too long
		{"01:000-2:00", ""},       // minute too long
		{"1pm,2pm", ""},           // not a range
		{"1pm 2pm", ""},           // not a range
		{"0", ""},                 // single number
		{"12", ""},                // single number
		{"99:00-02:00", ""},       // invalid hour
		{"02:00-99:00", ""},       // invalid hour
		{"02:00-a9:00", ""},       // invalid hour
		{"01:99-02:00", ""},       // invalid minute
		{"02:00-01:99", ""},       // invalid minute
		{"02:00-01:a9", ""},       // invalid minute
		{"02:30-", ""},            // open range
		{"-02:30", ""},            // open range
		{"2:00am-99:00", ""},      // misc
		{"01:00-02:00-03:00", ""}, // more than two components

		// valid 24h
		{"00:00-23:59", "00:00 - 23:59"},
		{"05:00-17:00", "05:00 - 17:00"},
		{"05-17", "05:00 - 17:00"},
		{"1-3", "01:00 - 03:00"},

		// valid 12h
		{"3:12am-11:23am", "03:12 - 11:23"},
		{"3:12pm-11:23pm", "15:12 - 23:23"},
		{"12:34am-5:43pm", "00:34 - 17:43"},
		{"12am-12pm", "00:00 - 12:00"},
		{"12pm-12am", "12:00 - 00:00"},
		{"03:00am-05:00am", "03:00 - 05:00"},
		{"03:00pm-05:00pm", "15:00 - 17:00"},

		// valid french
		{"0h00-1h00", "00:00 - 01:00"},
		{"00h00-1h00", "00:00 - 01:00"},
		{"5h12-23h15", "05:12 - 23:15"},

		// valid military
		{"0000-0100", "00:00 - 01:00"},
		{"0512-2315", "05:12 - 23:15"},

		// special
		{"noon-midnight", "12:00 - 00:00"},

		// special implies am/pm
		{"midnight - noon", "00:00 - 12:00"},
		{"noon-1:00", ""},
		{"1:00 - noon", "13:00 - 12:00"},
		{"1:00 am - noon", "01:00 - 12:00"},

		// next-day logic
		{"12:59-4:00am", "00:59 - 04:00"},
		{"12:59-4:00pm", "12:59 - 16:00"},
		{"3:30am-2:30pm", "03:30 - 14:30"},

		// am/pm assumption and next-day logic, h2>h1
		{"3-5", "03:00 - 05:00"},
		{"3-5am", "03:00 - 05:00"},
		{"3am-5", ""},
		{"3-5pm", "15:00 - 17:00"},
		{"3pm-5", ""},
		{"3am-5pm", "03:00 - 17:00"},
		{"3pm-5am", "15:00 - 05:00"},

		// am/pm assumption and next-day logic, h1>h2
		{"5-3", "05:00 - 03:00"},
		{"5-3am", "05:00 - 03:00"},
		{"5am-3", ""},
		{"5-3pm", "17:00 - 15:00"},
		{"5pm-3", ""},
		{"5am-3pm", "05:00 - 15:00"},
		{"5pm-3am", "17:00 - 03:00"},

		// misc ambiguous mixed 24h/12h
		{"23:03-5pm", ""},
		{"5pm-23:03", ""},
		{"noon-6:00", ""},
		{"noon-06:00", ""},
		{"6:00-noon", "18:00 - 12:00"},
		{"06:00-noon", "18:00 - 12:00"},
		{"23:00-noon", ""},

		// misc special
		{"noon-12:55pm", "12:00 - 12:55"},
		{"midnight-12:55am", "00:00 - 00:55"},

		// typo correction for extraneous separators
		{"01:00-02:00", "01:00 - 02:00"},
		{"01:00--02:00", "01:00 - 02:00"},
		{"01:00- -02:00", "01:00 - 02:00"},
		{"01:00 - - 02:00", "01:00 - 02:00"},
		{"01:00 - x - 02:00", ""},
		{"01:00 to 02:00 am", "01:00 - 02:00"},
		{"01:00 - to 02:00 am", "01:00 - 02:00"},
		{"01:00 to - 02:00 am", "01:00 - 02:00"},
		{"01:00 - to - 02:00 am", "01:00 - 02:00"},
		{"01:00 to - to 02:00 am", "01:00 - 02:00"},

		// typo correction for duplicate am/pm suffixes
		{"3:12am-11:23am am", "03:12 - 11:23"},
		{"3:12pm-11:23pm pm", "15:12 - 23:23"},
		{"3:12am am-11:23am am", "03:12 - 11:23"},
		{"3:12pm pm-11:23pm pm", "15:12 - 23:23"},
		{"3:12am am-11:23am", "03:12 - 11:23"},
		{"3:12pm pm-11:23pm", "15:12 - 23:23"},
		{"12:34am am-5:43pm pm", "00:34 - 17:43"},
		{"12:34amam-5:43pmpm", "00:34 - 17:43"},
		{"12:34aam-5:43ppm", ""},
		{"12:34aa-5:43pp", ""},
		{"1:00 am am - noon", "01:00 - 12:00"},
		{"1:00 am - noon pm", ""},

		// text normalization
		{"  \x1b1:00pm \u2013\n  \u00a02:\u200b00\x00 am", "13:00 - 02:00"},
		{"Noon - Midnight", "12:00 - 00:00"},
		{"Noon to Midnight", "12:00 - 00:00"},
	} {
		c, ok := parseClockRange(tc.A)
		if tc.B == "" {
			if ok {
				t.Errorf("parse %q: expected error, got %q (%#v)", tc.A, c.Format(false), c)
			}
			continue
		}
		if !ok {
			t.Errorf("parse %q: unexpected error", tc.A)
			continue
		}
		if s := c.Format(false); tc.B != s {
			t.Errorf("parse %q: expected %q, got %q (%#v)", tc.A, tc.B, s, c)
		}
		if c.Start >= 24*60 {
			t.Errorf("parse %q: start time should be in current day", tc.A)
		}
		if c.End >= 2*24*60 {
			t.Errorf("parse %q: start time should be before end of next day", tc.A)
		}
	}
}

func TestParseDateRange(t *testing.T) {
	for _, tc := range []struct {
		S        string // delimit prefix/range with {}
		From, To schema.Date
	}{
		// a representative sample of real examples
		//
		// printf '%s:data.json\n' $(git -C data log --pretty=format:%H data.json) | xargs git show | jq -r '.facilities[].scheduleGroups[].schedules[].caption' | sort -u
		{"Alexander Community Centre - racquet sports", 0, 0},
		{"Bearbrook Outdoor Pool - swim and aquafit{ - }August 30 to September 1", 8_30_0, 9_01_0},
		{"Bearbrook Outdoor Pool - swimming{ - }August 2 to 4", 8_02_0, 8_04_0},
		{"Bearbrook Outdoor Pool - swimming{ - }Tuesday, July 1", 7_01_3, 7_01_3},
		{"Beaverbrook outdoor pool - swim{ - }June 14 to 29", 6_14_0, 6_29_0},
		{"Bob MacQuarrie Recreation Complex - Orléans - group fitness{ - }starting September 8", 9_08_0, 0_0},
		{"Bob MacQuarrie Recreation Complex-Orléans - Skating{ - }September 3, 2025 to March 29, 2026", 2025_09_03_0, 2026_03_29_0},
		{"Canterbury Pool - all drop-ins{ - }July 1", 7_01_0, 7_01_0},
		{"Diane Deans Greenboro Community Centre - weight and cardio room{ - }until June 29", 0, 6_29_0},
		{"Kanata Seniors Centre - weekly drop-in activities{ - }June, July, August", 0, 0},
		{"Nepean Seniors Centre", 0, 0},
		{"Nepean Sportsplex - racquet sports{ - }May 17 to 19", 5_17_0, 5_19_0},
		{"Plant Recreation Centre - group fitness{ - }Monday, August 25 to Friday, August 29", 8_25_2, 8_29_6},
		{"Ray Friel Recreation Complex - skating - Labour Day", 0, 0},
		{"Walter Baker Sports Centre - Weight and cardio room", 0, 0},
		{"Plant Recreation Centre - all drop-ins{ - }August 30 and 31", 8_30_0, 8_31_0},

		// synthetic test cases
		{"test{ - }dummy January 1", 0, 0},
		{"test{ - }until January 1", 0, 1_01_0},
		{"test{ - }January", 0, 0},
		{"test{ - }January - January", 0, 0},
		{"test{ - }January, January", 0, 0},
		{"test{ - }January 1 and 1", 0, 0},
		{"test{ - }January 1 and 2", 1_01_0, 1_02_0},
		{"test{ - }January 1 and 3", 0, 0},
		// TODO: more
	} {
		tcP, sep, _ := strings.Cut(tc.S, "{")
		sep, tcR, _ := strings.Cut(sep, "}")
		tc.S = strings.ReplaceAll(strings.ReplaceAll(tc.S, "{", ""), "}", "")
		_ = sep

		if tc.S == "" && cmp.Or(tcP, tcR) != "" {
			panic("invalid test case")
		}
		if tcR == "" && cmp.Or(tc.From, tc.To) != 0 {
			panic("invalid test case")
		}
		if tcR == "" && tc.S != tcP {
			panic("invalid test case")
		}
		prefix, dates, ok := cutDateRange(tc.S)
		if !ok {
			if tcR != "" {
				t.Errorf("expected cut(%q) to match, got none", tc.S)
			}
			continue
		}
		if tcR == "" {
			t.Errorf("expected cut(%q) to not match, got (%q, %q)", tc.S, prefix, dates)
			continue
		}
		if tcP != prefix || tcR != dates {
			t.Errorf("expected cut(%q) to be (%q, %q), got (%q, %q)", tc.S, tcP, tcR, prefix, dates)
			continue
		}
		r, ok := parseDateRange(dates)
		if !ok {
			if tc.From != 0 || tc.To != 0 {
				t.Errorf("expected parse(%q) to succeed", dates)
			}
			continue
		}
		if tc.From == 0 && tc.To == 0 {
			t.Errorf("expected parse(%q) to fail, got %q(%#v,%#v)", dates, r.String(), r.From, r.To)
		}
		if tc.From != r.From || tc.To != r.To {
			t.Errorf("expected parse(%q) to be %q(%#v,%#v), got %q(%#v,%#v)", dates, schema.DateRange{From: tc.From, To: tc.To}.String(), tc.From, tc.To, r.String(), r.From, r.To)
		}
		if r.From != 0 {
			if _, ok := r.From.Month(); !ok {
				t.Errorf("bad invariant: parseDateRange should have a month set on from")
			}
			if _, ok := r.From.Day(); !ok {
				t.Errorf("bad invariant: parseDateRange should have a day set on from")
			}
		}
		if r.To != 0 {
			if _, ok := r.To.Month(); !ok {
				t.Errorf("bad invariant: parseDateRange should have a month set on to")
			}
			if _, ok := r.To.Day(); !ok {
				t.Errorf("bad invariant: parseDateRange should have a day set on to")
			}
		}
	}
}
