package ui

import (
	"testing"
	"time"
)

func TestFormat(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.00 KiB", 1536 << 20: "1.50 GiB", -2048: "-2.00 KiB"} {
		if got := Bytes(n); got != want {
			t.Errorf("Bytes(%d)=%q want %q", n, got, want)
		}
	}
	if Duration(3723*time.Second) != "1:02:03" || Duration(65*time.Second) != "1:05" {
		t.Error("duration")
	}
	if Ago(time.Time{}) != "never" || Ago(time.Now()) != "just now" || Time(time.Time{}) != "-" {
		t.Error("ago/time")
	}
	if Ratio(300, 100) != "3.00x" || Ratio(1, 0) != "-" {
		t.Error("ratio")
	}
}
