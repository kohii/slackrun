package util

import (
	"fmt"
	"time"
)

// FormatDuration renders a coarse "Xm Ys" style string suitable for progress
// updates ("⏳ Working… 1m 23s"). Sub-second precision is rounded down.
func FormatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d / time.Second)
	m := total / 60
	s := total % 60
	switch {
	case m == 0:
		return fmt.Sprintf("%ds", s)
	case s == 0:
		return fmt.Sprintf("%dm", m)
	default:
		return fmt.Sprintf("%dm %ds", m, s)
	}
}
