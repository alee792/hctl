package main

import "testing"

func TestForegroundOperationsRetainDefaultTerminalSignals(t *testing.T) {
	for _, test := range []struct {
		args []string
		want bool
	}{
		{args: []string{"hctl-discord", "setup", "--profile", "default"}, want: true},
		{args: []string{"hctl-discord", "remove", "--profile", "default"}, want: true},
		{args: []string{"hctl-discord", "status", "--profile", "default"}, want: false},
		{args: []string{"hctl-discord", "runtime"}, want: false},
		{args: []string{"hctl-discord"}, want: false},
	} {
		if got := usesForegroundTerminal(test.args); got != test.want {
			t.Fatalf("usesForegroundTerminal(%q) = %t, want %t", test.args, got, test.want)
		}
	}
}
