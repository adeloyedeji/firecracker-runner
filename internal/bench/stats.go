package bench

import (
	"fmt"
	"io"
	"math"
	"sort"
	"text/tabwriter"
	"time"
)

// Summary holds distribution stats for one phase.
type Summary struct {
	N                            int
	Min, P50, P90, P99, Max, Avg time.Duration
}

func summarize(ds []time.Duration) Summary {
	if len(ds) == 0 {
		return Summary{}
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	var sum time.Duration
	for _, d := range s {
		sum += d
	}
	return Summary{
		N:   len(s),
		Min: s[0],
		P50: pct(s, 50),
		P90: pct(s, 90),
		P99: pct(s, 99),
		Max: s[len(s)-1],
		Avg: sum / time.Duration(len(s)),
	}
}

// pct uses the nearest-rank method on a sorted slice.
func pct(sorted []time.Duration, p float64) time.Duration {
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}

func fmtDur(d time.Duration) string {
	return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
}

// PrintTable writes a per-phase summary of all successful runs.
func PrintTable(w io.Writer, runs []Run) {
	var ok []Run
	for _, r := range runs {
		if r.Err == "" {
			ok = append(ok, r)
		}
	}
	fmt.Fprintf(w, "\n%d/%d runs succeeded\n\n", len(ok), len(runs))
	if len(ok) == 0 {
		return
	}

	phases := []struct {
		name string
		get  func(Run) time.Duration
	}{
		{"spawn -> api socket", func(r Run) time.Duration { return r.Spawn }},
		{"snapshot/load (API)", func(r Run) time.Duration { return r.Load }},
		{"resume -> guest ready", func(r Run) time.Duration { return r.Ready }},
		{"TOTAL", func(r Run) time.Duration { return r.Total }},
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "phase\tmin\tp50\tp90\tp99\tmax\tavg\t")
	for _, ph := range phases {
		ds := make([]time.Duration, len(ok))
		for i, r := range ok {
			ds[i] = ph.get(r)
		}
		s := summarize(ds)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t\n", ph.name,
			fmtDur(s.Min), fmtDur(s.P50), fmtDur(s.P90), fmtDur(s.P99), fmtDur(s.Max), fmtDur(s.Avg))
	}
	tw.Flush()

	var minf, majf uint64
	for _, r := range ok {
		minf += r.MinorFaults
		majf += r.MajorFaults
	}
	n := uint64(len(ok))
	fmt.Fprintf(w, "\navg page faults per run (VMM process): minor=%d major=%d\n", minf/n, majf/n)
	if v, ok := ok[0].FCLatenciesUs["load_snapshot"]; ok {
		fmt.Fprintf(w, "firecracker-reported load_snapshot (run 1): %vus\n", v)
	}
}
