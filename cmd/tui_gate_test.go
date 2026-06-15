package cmd

import "testing"

func TestShouldRunTUI(t *testing.T) {
	cases := []struct {
		name string
		tty  TTYState
		term string
		want bool
	}{
		{"tty_xterm", TTYState{StdoutIsTTY: true}, "xterm-256color", true},
		{"not_tty", TTYState{StdoutIsTTY: false}, "xterm-256color", false},
		{"term_dumb", TTYState{StdoutIsTTY: true}, "dumb", false},
		{"term_empty", TTYState{StdoutIsTTY: true}, "", false},
		{"not_tty_and_dumb", TTYState{StdoutIsTTY: false}, "dumb", false},
		{"tty_ansi", TTYState{StdoutIsTTY: true}, "ansi", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldRunTUI(tc.tty, tc.term)
			if got != tc.want {
				t.Errorf("shouldRunTUI(%+v, %q) = %v, want %v", tc.tty, tc.term, got, tc.want)
			}
		})
	}
}
