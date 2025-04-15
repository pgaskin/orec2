package schema

import "testing"

func TestClockTime(t *testing.T) {
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
