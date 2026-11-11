package bench

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestSummarize(t *testing.T) {
	var ds []time.Duration
	for i := 1; i <= 100; i++ {
		ds = append(ds, time.Duration(i)*time.Millisecond)
	}
	s := summarize(ds)
	if s.Min != time.Millisecond || s.Max != 100*time.Millisecond ||
		s.P50 != 50*time.Millisecond || s.P99 != 99*time.Millisecond {
		t.Fatalf("bad summary %+v", s)
	}
}

func TestPrintTable(t *testing.T) {
	runs := []Run{
		{Index: 1, Spawn: 5 * time.Millisecond, Load: 8 * time.Millisecond, Ready: 3 * time.Millisecond, Total: 16 * time.Millisecond},
		{Index: 2, Err: "boom"},
	}
	var b bytes.Buffer
	PrintTable(&b, runs)
	out := b.String()
	if !strings.Contains(out, "1/2 runs succeeded") || !strings.Contains(out, "16.00ms") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}
