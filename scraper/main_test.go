package main

import "testing"

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
