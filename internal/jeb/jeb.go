// Package jeb asks Jebadiah, a decision model served by llama.cpp's
// llama-server, a typed question about a state.
//
// It ports the path of the model card's decide_gguf.py (frontier-infra/
// jebadiah-4b-v2-GGUF): AINode's build_messages, the Qwen chat template with
// thinking off, then one forward pass. Nothing is generated. The answer is
// read off the log probabilities of the single-token option labels ("A",
// "B", ...) at the answer position, renormalised over the labels, with the
// model's per-type temperature applied.
package jeb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// Pin identifies the exact weights. Every result records it.
type Pin struct {
	Repo       string `json:"repo"`
	Revision   string `json:"revision"`
	File       string `json:"file"`
	FileSHA256 string `json:"file_sha256"`
	BaseRepo   string `json:"base_repo"`
	BaseSHA    string `json:"base_revision"`
}

// Jebadiah4BV2 is the model ambit uses for Arm A-local and as Arm B's PIP.
var Jebadiah4BV2 = Pin{
	Repo:       "frontier-infra/jebadiah-4b-v2-GGUF",
	Revision:   "446099f37ca1cea4324d8866b44a1dbc15f77c94",
	File:       "jebadiah-4b-v2-Q8_0.gguf",
	FileSHA256: "36f4a54fe07ed79b186a2128bfb1598ba210b1ab0a945296069a4e5cdbb9875a",
	BaseRepo:   "frontier-infra/jebadiah-4b-v2",
	BaseSHA:    "af540a995db73228cc89957429044a8ae3f0a600",
}

// Temperatures from the pinned revision's temperatures.json.
var Temperatures = map[string]float64{"choice": 1.1167, "noul": 1.3319, "score": 0.8312}

// AINode's prompt text (ainode_prompt_verbatim.py, source commit
// e5c089386e0239c9eb270eeb490d181722b8da5b).
const (
	systemPrompt      = "You are a decision function. Answer with the single letter of the best option and nothing else."
	answerInstruction = "Answer with the label of one option and nothing else."
)

// Option is one answer the model can give: a name and an optional
// description.
type Option struct {
	Name        string
	Description string
}

// Question is a typed question. Type is "choice" or "noul"; a noul's options
// are always true then false.
type Question struct {
	Type         string
	Instructions string
	Options      []Option // choice only; for a noul, optional descriptions of true and false
}

// Answer is the model's distribution over a question's options.
type Answer struct {
	// Probs maps each option name to its probability.
	Probs map[string]float64
	// Logprobs are the raw label log probabilities, in option order.
	Logprobs []float64
	// Missing counts labels not in the returned top-n; they got the floor.
	Missing int
	Prompt  string
	Elapsed time.Duration
}

// P returns the probability of "true" for a noul.
func (a Answer) P() float64 { return a.Probs["true"] }

// Timeout bounds one request. A swarm raises it (cmd/ambit swarm): its
// gate latency includes queueing at the server, so a call waits rather
// than fails.
var Timeout = 10 * time.Minute

// Client talks to one llama-server.
type Client struct {
	URL  string
	HTTP *http.Client
	// CachePrompt lets llama-server reuse the KV cache of a shared prompt
	// prefix (G2). It changes speed, not the forward pass; false is G1.
	CachePrompt bool
	labels      []int // token id of label i at the answer boundary
}

// New connects and resolves the label tokens.
func New(ctx context.Context, url string) (*Client, error) {
	c := &Client{URL: strings.TrimRight(url, "/"), HTTP: &http.Client{Timeout: Timeout}}
	if err := c.resolveLabels(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// OptionLabel is AINode's bijective base-26 label: A..Z, AA, AB, ...
func OptionLabel(i int) string {
	n := i + 1
	out := ""
	for n > 0 {
		n--
		out = string(rune('A'+n%26)) + out
		n /= 26
	}
	return out
}

func optionText(o Option) string {
	if d := strings.Join(strings.Fields(o.Description), " "); d != "" {
		return o.Name + ": " + d
	}
	return o.Name
}

func (q Question) options() []Option {
	if q.Type != "noul" {
		return q.Options
	}
	out := []Option{{Name: "true"}, {Name: "false"}}
	for _, o := range q.Options {
		switch o.Name {
		case "true":
			out[0].Description = o.Description
		case "false":
			out[1].Description = o.Description
		}
	}
	return out
}

// Render returns the prompt text for state and q: AINode's messages through
// the Qwen chat template with add_generation_prompt and thinking off. The
// template trims each message's content.
func Render(state string, q Question) string {
	lines := []string{"STATE:", state, "", "QUESTION: " + strings.TrimSpace(q.Instructions), "", "OPTIONS:"}
	for i, o := range q.options() {
		lines = append(lines, OptionLabel(i)+". "+optionText(o))
	}
	lines = append(lines, "", answerInstruction)
	user := strings.Join(lines, "\n")
	return "<|im_start|>system\n" + strings.TrimSpace(systemPrompt) + "<|im_end|>\n" +
		"<|im_start|>user\n" + strings.TrimSpace(user) + "<|im_end|>\n" +
		"<|im_start|>assistant\n<think>\n\n</think>\n\n"
}

// resolveLabels finds the token id of each label as the single token that
// follows a rendered prompt, as decide_gguf.py's Renderer does.
func (c *Client) resolveLabels(ctx context.Context) error {
	probe := Render("x", Question{Type: "choice", Instructions: "q?", Options: []Option{{Name: "o1"}, {Name: "o2"}}})
	base, err := c.tokenize(ctx, probe)
	if err != nil {
		return err
	}
	for i := 0; i < 26; i++ {
		ids, err := c.tokenize(ctx, probe+OptionLabel(i))
		if err != nil {
			return err
		}
		if len(ids) != len(base)+1 || !equal(ids[:len(base)], base) {
			return fmt.Errorf("label %s is not one token at the answer boundary", OptionLabel(i))
		}
		c.labels = append(c.labels, ids[len(base)])
	}
	return nil
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("llama-server %s: %s: %s", path, resp.Status, data)
	}
	return json.Unmarshal(data, out)
}

func (c *Client) tokenize(ctx context.Context, text string) ([]int, error) {
	var r struct {
		Tokens []int `json:"tokens"`
	}
	err := c.post(ctx, "/tokenize", map[string]any{"content": text}, &r)
	return r.Tokens, err
}

// Ask answers one question about state.
func (c *Client) Ask(ctx context.Context, state string, q Question) (Answer, error) {
	opts := q.options()
	if len(opts) < 2 || len(opts) > len(c.labels) {
		return Answer{}, fmt.Errorf("jeb: %d options", len(opts))
	}
	prompt := Render(state, q)
	start := time.Now()
	var r struct {
		Probs []struct {
			Top []struct {
				ID      int     `json:"id"`
				Logprob float64 `json:"logprob"`
			} `json:"top_logprobs"`
		} `json:"completion_probabilities"`
	}
	err := c.post(ctx, "/completion", map[string]any{
		"prompt": prompt, "n_predict": 1, "n_probs": 1000, "post_sampling_probs": false,
		"cache_prompt": c.CachePrompt, "temperature": 0.0,
	}, &r)
	elapsed := time.Since(start)
	if err != nil {
		return Answer{}, err
	}
	if len(r.Probs) == 0 || len(r.Probs[0].Top) == 0 {
		return Answer{}, fmt.Errorf("jeb: no log probabilities returned")
	}
	lp := map[int]float64{}
	floor := math.Inf(1)
	for _, t := range r.Probs[0].Top {
		lp[t.ID] = t.Logprob
		floor = math.Min(floor, t.Logprob)
	}
	a := Answer{Probs: map[string]float64{}, Prompt: prompt, Elapsed: elapsed}
	for i := range opts {
		v, ok := lp[c.labels[i]]
		if !ok {
			v = floor
			a.Missing++
		}
		a.Logprobs = append(a.Logprobs, v)
	}
	t := Temperatures[q.Type]
	if t == 0 {
		t = 1
	}
	for i, p := range softmax(a.Logprobs, t) {
		a.Probs[opts[i].Name] = p
	}
	return a, nil
}

func softmax(lp []float64, t float64) []float64 {
	m := math.Inf(-1)
	for _, x := range lp {
		m = math.Max(m, x/t)
	}
	out := make([]float64, len(lp))
	s := 0.0
	for i, x := range lp {
		out[i] = math.Exp(x/t - m)
		s += out[i]
	}
	for i := range out {
		out[i] /= s
	}
	return out
}
