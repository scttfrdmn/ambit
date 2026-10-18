package report

import (
	"fmt"
	"testing"
)

func TestRateAndInterval(t *testing.T) {
	var it []item
	for f := 0; f < 40; f++ {
		for k := 0; k < 10; k++ {
			it = append(it, item{fam: fmt.Sprint(f), in: true, hit: k < 3})
		}
	}
	iv := rate(it, 1)
	if iv.K != 120 || iv.N != 400 || iv.Est != 0.3 {
		t.Fatalf("got %+v", iv)
	}
	// Every family has the same rate, so the interval collapses.
	if iv.Lo != 0.3 || iv.Hi != 0.3 {
		t.Fatalf("interval %+v", iv)
	}
	if rate(nil, 1).N != 0 {
		t.Fatal("empty rate should be undefined")
	}
}

func TestIntervalWidensWithFamilyVariance(t *testing.T) {
	var it []item
	for f := 0; f < 40; f++ {
		for k := 0; k < 10; k++ {
			it = append(it, item{fam: fmt.Sprint(f), in: true, hit: f%2 == 0})
		}
	}
	iv := rate(it, 1)
	if iv.Est != 0.5 || iv.Lo >= 0.45 || iv.Hi <= 0.55 {
		t.Fatalf("family-clustered rate should have a wide interval: %+v", iv)
	}
}

func TestDiffPaired(t *testing.T) {
	var a, b []item
	for f := 0; f < 30; f++ {
		for k := 0; k < 10; k++ {
			a = append(a, item{fam: fmt.Sprint(f), in: true, hit: k < 1})
			b = append(b, item{fam: fmt.Sprint(f), in: true, hit: k < 5})
		}
	}
	d := diff(a, b, 1)
	if d.Est > -0.39 || d.Est < -0.41 || d.Hi >= 0 {
		t.Fatalf("diff %+v", d)
	}
}
