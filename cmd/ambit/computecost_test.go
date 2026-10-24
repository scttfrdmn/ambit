package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestReadJobLog(t *testing.T) {
	p := filepath.Join(t.TempDir(), "h.job.log")
	log := "2026-10-20T01:00:00Z host h\n" +
		"NVIDIA L40S, 46068 MiB, 595.91.07\n" +
		"progress line without a timestamp\n" +
		"2026-10-20T02:30:00Z done, exit 0\n"
	if err := os.WriteFile(p, []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	j, err := readJobLog(p)
	if err != nil {
		t.Fatal(err)
	}
	if j.gpu != "NVIDIA L40S" {
		t.Errorf("gpu = %q, want NVIDIA L40S", j.gpu)
	}
	if math.Abs(j.hours-1.5) > 1e-9 {
		t.Errorf("hours = %v, want 1.5", j.hours)
	}
	if _, ok := onDemandPerHour[j.gpu]; !ok {
		t.Errorf("no price for %q", j.gpu)
	}
}
