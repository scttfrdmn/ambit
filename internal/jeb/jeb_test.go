package jeb

import (
	"context"
	"math"
	"os"
	"testing"
)

func TestOptionLabel(t *testing.T) {
	for i, want := range map[int]string{0: "A", 25: "Z", 26: "AA", 27: "AB", 51: "AZ", 52: "BA"} {
		if got := OptionLabel(i); got != want {
			t.Errorf("OptionLabel(%d) = %s, want %s", i, got, want)
		}
	}
}

// TestPublishedExample reproduces the model card's output on
// scripts/example-request.json against a live llama-server serving the
// pinned file. Set AMBIT_LLAMA_URL to run it.
func TestPublishedExample(t *testing.T) {
	url := os.Getenv("AMBIT_LLAMA_URL")
	if url == "" {
		t.Skip("AMBIT_LLAMA_URL not set")
	}
	ctx := context.Background()
	c, err := New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	// serialize_state of {"ticket": ...}: compact JSON, sorted keys.
	state := `{"ticket":"Customer says the invoice total does not match the quote."}`
	route, err := c.Ask(ctx, state, Question{Type: "choice", Instructions: "Which team should take this ticket?", Options: []Option{
		{"billing", "an invoice, a charge or a refund"}, {"support", "a product question"}, {"sales", "a quote or a renewal"}}})
	if err != nil {
		t.Fatal(err)
	}
	urgent, err := c.Ask(ctx, state, Question{Type: "noul", Instructions: "The customer is blocked from working.", Options: []Option{
		{"true", "work has stopped"}, {"false", "it can wait"}}})
	if err != nil {
		t.Fatal(err)
	}
	// Published: billing 0.598244, support 0.082792, sales 0.318963; urgent 0.153519.
	want := map[string]float64{"billing": 0.598244, "support": 0.082792, "sales": 0.318963}
	for k, v := range want {
		if math.Abs(route.Probs[k]-v) > 1e-4 {
			t.Errorf("route %s = %.6f, published %.6f", k, route.Probs[k], v)
		}
	}
	if math.Abs(urgent.P()-0.153519) > 1e-4 {
		t.Errorf("urgent = %.6f, published 0.153519", urgent.P())
	}
	t.Logf("route %v urgent %.6f (%s, %s)", route.Probs, urgent.P(), route.Elapsed, urgent.Elapsed)
}
