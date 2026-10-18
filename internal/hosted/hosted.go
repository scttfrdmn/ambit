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
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
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
	// Attempts counts HTTP attempts, the first included.
	Attempts int
}

// Client calls the Decisions API.
type Client struct {
	BaseURL string
	Key     string
	HTTP    *http.Client
	Ledger  *Ledger
	// Backoff is the first retry's wait before jitter; 0 means one second.
	Backoff time.Duration
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

// MaxAttempts bounds the HTTP attempts per call, the first included.
const MaxAttempts = 10

// Ask asks one predicate about state. Rate limits (a 429 that is not a
// quota or billing error), 500, 502, 503, 504, and transport errors are
// retried with exponential backoff and jitter, honouring Retry-After, up to
// MaxAttempts. The ledger reservation is held across retries and released
// if no attempt succeeds. Elapsed covers every attempt and wait.
func (c *Client) Ask(ctx context.Context, state, instructions string) (Answer, error) {
	est := Cost(EstimateTokens(state, instructions))
	if err := c.Ledger.Reserve(est); err != nil {
		return Answer{}, err
	}
	body, err := json.Marshal(request{Model: Model, Input: state,
		Questions: []question{{Type: "predicate", Name: "q", Instructions: instructions}}})
	if err != nil {
		c.Ledger.Release(est)
		return Answer{}, err
	}
	start := time.Now()
	var res result
	attempts := 0
	for {
		attempts++
		res, err = c.post(ctx, body)
		if err == nil && res.code == http.StatusOK {
			break
		}
		if err == nil {
			err = fmt.Errorf("decisions API: %s: %s", res.status, res.raw)
		}
		if ctx.Err() != nil || attempts >= MaxAttempts || !retryable(res.code, res.raw) {
			c.Ledger.Release(est)
			if attempts > 1 {
				err = fmt.Errorf("%w (after %d attempts)", err, attempts)
			}
			return Answer{Raw: res.raw, Attempts: attempts}, err
		}
		select {
		case <-time.After(c.backoff(attempts, res.retryAfter)):
		case <-ctx.Done():
			c.Ledger.Release(est)
			return Answer{Raw: res.raw, Attempts: attempts}, ctx.Err()
		}
	}
	elapsed := time.Since(start)
	raw := res.raw
	var r response
	if err := json.Unmarshal(raw, &r); err != nil {
		c.Ledger.Release(est)
		return Answer{Raw: raw, Attempts: attempts}, fmt.Errorf("decisions API: decode: %w", err)
	}
	a := Answer{
		ServedModel: r.Model, InputTokens: r.Usage.InputTokens, CostMicros: Cost(r.Usage.InputTokens),
		Date: start.UTC().Format("2006-01-02"), Raw: raw, Elapsed: elapsed, RequestBytes: body,
		Attempts: attempts,
	}
	c.Ledger.Settle(est, a.CostMicros)
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

// result is one attempt's HTTP response. code is 0 for a transport error.
type result struct {
	code       int
	status     string
	retryAfter string
	raw        []byte
}

// post makes one attempt. A transport error is returned as err.
func (c *Client) post(ctx context.Context, body []byte) (result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/decisions", bytes.NewReader(body))
	if err != nil {
		return result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return result{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return result{}, err
	}
	return result{code: resp.StatusCode, status: resp.Status, retryAfter: resp.Header.Get("Retry-After"), raw: raw}, nil
}

// retryable reports whether an attempt may succeed if repeated. Code 0 is a
// transport error. A 429 that reports exhausted quota or credit would fail
// again; any other 429 is a rate limit.
func retryable(code int, raw []byte) bool {
	switch code {
	case 0, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	case http.StatusTooManyRequests:
		return !bytes.Contains(raw, []byte("insufficient_quota")) && !bytes.Contains(raw, []byte("credit_balance_exhausted"))
	}
	return false
}

// maxBackoff bounds any one wait, Retry-After included.
const maxBackoff = 2 * time.Minute

// backoff is the wait after the given failed attempt: the server's
// Retry-After if it sent one, else Backoff·2^(attempt-1) jittered over its
// upper half.
func (c *Client) backoff(attempt int, retryAfter string) time.Duration {
	if retryAfter != "" {
		if s, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && s >= 0 {
			return min(time.Duration(s)*time.Second, maxBackoff)
		}
		if t, err := http.ParseTime(retryAfter); err == nil {
			return min(max(time.Until(t), 0), maxBackoff)
		}
	}
	base := c.Backoff
	if base <= 0 {
		base = time.Second
	}
	d := min(base<<(attempt-1), maxBackoff)
	return d/2 + rand.N(d/2+1)
}
