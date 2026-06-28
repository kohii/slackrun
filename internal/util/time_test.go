package util

import (
	"testing"
	"time"
)

func TestFormatDuration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{-time.Second, "0s"},
		{500 * time.Millisecond, "0s"},
		{45 * time.Second, "45s"},
		{60 * time.Second, "1m"},
		{90 * time.Second, "1m 30s"},
		{3600 * time.Second, "60m"},
	}
	for _, c := range cases {
		got := FormatDuration(c.in)
		if got != c.want {
			t.Fatalf("FormatDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
