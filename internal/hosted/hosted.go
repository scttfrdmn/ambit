// Package hosted asks OpenAI's Decisions API (POST /v1/decisions) a
// predicate about a state. It is a thin HTTP client: the request carries the
// state as a plain-string input and one predicate question; the response's
// served model ID, token usage, and raw bytes are returned for the record.
//
// Hosted aliases are not snapshots, so every answer records the model the
// server says it used and the date (Law 5). Every call is charged against a
// Ledger before it is made (Law 6).
package hosted

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Model is the requested model. gpt-6-luna is the only Decisions model.
const Model = "gpt-6-luna"

// InputMicrosPerMTok is the price of input tokens in micro-dollars per
// million tokens ($0.10/M). Output is not billed.
const InputMicrosPerMTok = 100_000

// Answer is one predicate answer.
type Answer struct {
	P            float64
	Refused      bool
	ServedModel  string
	InputTokens  int64
	CostMicros   int64
	Date         string // UTC date of the call
	Raw          json.RawMessage
	Elapsed      time.Duration
	RequestBytes []byte
}

// Client calls the Decisions API.
type Client struct {
	BaseURL string
	Key     string
	HTTP    *http.Client
	Ledger  *Ledger
}

// FromEnv builds a client from OPENAI_API_KEY (and OPENAI_BASE_URL, for
// tests).
func FromEnv(l *Ledger) (*Client, error) {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY is not set")
	}
	base := os.Getenv("OPENAI_BASE_URL")
	if base == "" {
		base = "https://api.openai.com"
	}
	return &Client{BaseURL: strings.TrimRight(base, "/"), Key: key, HTTP: &http.Client{Timeout: 2 * time.Minute}, Ledger: l}, nil
}

type request struct {
	Model     string     `json:"model"`
	Input     string     `json:"input"`
	Questions []question `json:"questions"`
}

type question struct {
	Type         string `json:"type"`
	Name         string `json:"name"`
	Instructions string `json:"instructions"`
}

type response struct {
	Answers []struct {
		Type        string  `json:"type"`
		Name        string  `json:"name"`
		Probability float64 `json:"probability"`
	} `json:"answers"`
	Model string `json:"model"`
	Usage struct {
		InputTokens int64 `json:"input_tokens"`
	} `json:"usage"`
}

// EstimateTokens is a conservative pre-call token estimate used for the
// spend check: one token per three bytes.
func EstimateTokens(state, instructions string) int64 {
	return int64(len(state)+len(instructions))/3 + 64
}

// Cost returns the cost of n input tokens in micro-dollars, rounded up.
func Cost(n int64) int64 { return (n*InputMicrosPerMTok + 999_999) / 1_000_000 }

// Ask asks one predicate about state.
func (c *Client) Ask(ctx context.Context, state, instructions string) (Answer, error) {
	if err := c.Ledger.Reserve(Cost(EstimateTokens(state, instructions))); err != nil {
		return Answer{}, err
	}
	body, err := json.Marshal(request{Model: Model, Input: state,
		Questions: []question{{Type: "predicate", Name: "q", Instructions: instructions}}})
	if err != nil {
		return Answer{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/decisions", bytes.NewReader(body))
	if err != nil {
		return Answer{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Answer{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	elapsed := time.Since(start)
	if err != nil {
		return Answer{}, err
	}
	if resp.StatusCode != http.StatusOK {
		c.Ledger.Release()
		return Answer{Raw: raw}, fmt.Errorf("decisions API: %s: %s", resp.Status, raw)
	}
	var r response
	if err := json.Unmarshal(raw, &r); err != nil {
		return Answer{Raw: raw}, fmt.Errorf("decisions API: decode: %w", err)
	}
	a := Answer{
		ServedModel: r.Model, InputTokens: r.Usage.InputTokens, CostMicros: Cost(r.Usage.InputTokens),
		Date: start.UTC().Format("2006-01-02"), Raw: raw, Elapsed: elapsed, RequestBytes: body,
	}
	c.Ledger.Settle(a.CostMicros)
	if len(r.Answers) != 1 {
		return a, fmt.Errorf("decisions API: %d answers", len(r.Answers))
	}
	switch r.Answers[0].Type {
	case "predicate":
		a.P = r.Answers[0].Probability
	case "refusal":
		a.Refused = true
	default:
		return a, fmt.Errorf("decisions API: answer type %q", r.Answers[0].Type)
	}
	return a, nil
}
