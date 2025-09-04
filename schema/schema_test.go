package schema

import (
	"strings"
	"testing"
	"time"
)

func TestClockTime(t *testing.T) {
	if !strings.HasSuffix(ClockTime(60*34+56).GoString(), "ClockTime(60*34+56)") {
		t.Fatal("bad gostring")
	}

	for _, tc := range []struct {
		HH1, MM1 int
		HH2, MM2 int
		Result   string
		S1, S2   string
	}{
		{0, 0, 1, 0, "12:00 - 1:00am", "00:00", "01:00"},
		{12, 0, 1, 0, "12:00pm - 1:00am", "12:00", ">01:00"},
		{9, 59, 14, 2, "9:59am - 2:02pm", "09:59", "14:02"},
		{14, 2, 9, 59, "2:02pm - 9:59am", "14:02", ">09:59"},
		{-1, 0, 1, 0, "invalid", "invalid", "01:00"},
		{0, 0, 22, 0, "12:00am - 10:00pm", "00:00", "22:00"},
		{22, 0, 44, 0, "10:00 - 8:00pm", "22:00", ">20:00"},
		{44, 0, 66, 0, ">8:00pm - >>6:00pm", ">20:00", ">>18:00"},
		{0, 0, 35, 0, "12:00am - >11:00am", "00:00", ">11:00"},
	} {
		tr := MakeClockRange(tc.HH1, tc.MM1, tc.HH2, tc.MM2)
		if act := tr.String(); act != tc.Result {
			t.Errorf("%02d:%02d - %02d:%02d = %q, got %q", tc.HH1, tc.MM1, tc.HH2, tc.MM2, tc.Result, act)
		} else {
			t.Logf("%02d:%02d - %02d:%02d = %q", tc.HH1, tc.MM1, tc.HH2, tc.MM2, tc.Result)
		}
		if act := tr.Start.Format(false); tc.S1 != act {
			t.Errorf("%d = %q, got %q", tr.Start, tc.S1, act)
		} else {
			t.Logf("%d = %q", tr.Start, tc.S1)
		}
		if act := tr.End.Format(false); tc.S2 != act {
			t.Errorf("%d = %q, got %q", tr.End, tc.S2, act)
		} else {
			t.Logf("%d = %q", tr.End, tc.S2)
		}
	}
}

func TestDate(t *testing.T) {
	tmp := Date(2222_11_21_3)
	if x, ok := tmp.Year(); !ok || x != 2222 {
		t.Fatal("bad year splitting")
	}
	if x, ok := tmp.Month(); !ok || x != time.November {
		t.Fatal("bad month splitting")
	}
	if x, ok := tmp.Day(); !ok || x != 21 {
		t.Fatal("bad day splitting")
	}
	if x, ok := tmp.Weekday(); !ok || x != time.Weekday(2) {
		t.Fatal("bad weekday splitting")
	}
	if !strings.HasSuffix(tmp.GoString(), "Date(2222_11_21_3)") {
		t.Fatal("bad gostring")
	}

	for _, tc := range []struct {
		D1, D2 Date
		Result string
		S1, S2 string
	}{
		{2025_00_00_0, 0, "", "", "2025"},
		{2025_01_00_0, 0, "", "", "January 2025"},
		{2025_01_01_0, 0, "", "", "January 1, 2025"},
		{2025_01_01_4, 0, "", "", "Wednesday, January 1, 2025"},
		{1_00_0, 0, "", "", "January"},
		{1_01_0, 0, "", "", "January 1"},
		{1_01_4, 0, "", "", "Wednesday, January 1"},
		{1_0, 0, "", "", ""},          // we don't output the day number if no month
		{1_4, 0, "", "", "Wednesday"}, // we don't output the day number if no month
		{0, 0, "", "", ""},
		{4, 0, "", "", "Wednesday"},

		{1_02_0, 3_04_0, "January 2 to March 4", "January 2", "March 4"},
		{0, 3_04_0, "until March 4", "", "March 4"},
		{1_02_0, 0, "starting January 2", "January 2", ""},
	} {
		if tc.D1 != 0 && tc.D2 == 0 && tc.Result == tc.S1 { // shorthand test cases for single dates
			tc.D2 = tc.D1
			tc.S1 = tc.S2
			tc.Result = tc.S1
		}
		dr := DateRange{tc.D1, tc.D2}
		if act := dr.String(); act != tc.Result {
			t.Errorf("%09d - %09d = %q, got %q", dr.From, dr.To, tc.Result, act)
		} else {
			t.Logf("%09d - %09d = %q", dr.From, dr.To, tc.Result)
		}
		if act := dr.From.String(); tc.S1 != act {
			t.Errorf("%09d = %q, got %q", dr.From, tc.S1, act)
		} else {
			t.Logf("%09d = %q", dr.From, tc.S1)
		}
		if act := dr.To.String(); tc.S2 != act {
			t.Errorf("%09d = %q, got %q", dr.To, tc.S2, act)
		} else {
			t.Logf("%09d = %q", dr.To, tc.S2)
		}
	}
}
