// Package strands asks strands-decider, a decision model with a pointer
// head served by the strands-decider package's own server, typed questions
// about a state.
//
// The wire format is the server's POST /v1/systemone (src/strands_decider/
// schema.py at the pinned server commit): one state, any number of named
// questions, one forward pass that encodes the state once. The server
// renders its own prompt; nothing of internal/jeb's rendering applies.
//
// The server has documented concurrency bugs (issue #9, an MPS abort on
// overlapping requests, and a race on every device that returned wrong
// probabilities, fixed in 6039cf8b64). The client therefore sends one
// request at a time, as the upstream example client recommends.
package strands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Pin identifies the exact weights and server. Every result records it.
type Pin struct {
	Repo         string `json:"repo"`
	Revision     string `json:"revision"`
	BaseRepo     string `json:"base_repo"`
	BaseRevision string `json:"base_revision"`
	// Server is the strands-decider package, as git repo@commit.
	Server string `json:"server"`
}

// D3Q8 is Decision 3.0 d3 (vllm-sr/d3) as ggml-org's Q8_0 GGUF, served by
// llama.cpp's /v1/systemone (build b11669, the build ambit pins). It speaks
// the same systemone wire as strands-decider, so this client serves it. Run
// single-slot (-np 1): llama.cpp issue #30402 reports d3 answers changing
// when requests share a batch. Registration #33, item 14.
var D3Q8 = Pin{
	Repo:         "ggml-org/vllm-d3-GGUF/vllm-d3-Q8_0.gguf",
	Revision:     "de9670136f5ddd83e677c89ce041d5b9bb3b0dcb",
	BaseRepo:     "vllm-sr/d3",
	BaseRevision: "cf15549a78fc52598ddd1951f321c58372bbd64f",
	Server:       "llama.cpp b11669 (620d2225) server-cuda image sha256:e4cfff28678327b0ae7eda129e700e56a0fa55aa23db2f016256ea155c15c2b5; GGUF sha256 82130d83cb6f31d43a742a08f737a78c087e408776115b6f28e5d3bc27617805",
}

// D3Official is d3 in BF16 with the model repository's own server
// (d3_server.py, transformers 5.17.0), for the fidelity check of D3Q8.
var D3Official = Pin{
	Repo:         "vllm-sr/d3",
	Revision:     "cf15549a78fc52598ddd1951f321c58372bbd64f",
	BaseRepo:     "vllm-sr/d3",
	BaseRevision: "cf15549a78fc52598ddd1951f321c58372bbd64f",
	Server:       "d3_server.py from the model repository at cf15549a; transformers==5.17.0, flash-linear-attention==0.5.2",
}

// ID names the model and its pin.
func (p Pin) ID() string { return p.Repo + "@" + p.Revision }

// HobsonV21 is the model ambit uses for Arms A-strands and B-strands.
var HobsonV21 = Pin{
	Repo:     "StrandsAgents/strands-decider-2B-hobson-v21",
	Revision: "2b52a6235c1b8306bbfa30b00b9d4b74b63a39f5",
	BaseRepo: "Qwen/Qwen3.5-2B-Base",
	// The model's provenance.json gives this base revision as inferred,
	// not recorded at training time.
	BaseRevision: "b1485b2fa6dfa1287294f269f5fb618e03d52d7c",
	Server:       "strands-labs/strands-decider@930e6d5de16309be32579f5631a7369a3322140c",
}

// RequestModel is sent as the request's model. The server ignores it and
// answers as the checkpoint it loaded; this is the schema's default.
const RequestModel = "strands-decider-latest"

// Timeout bounds one request, as in the upstream example client. A swarm
// raises it (cmd/ambit swarm), as for internal/jeb.
var Timeout = 120 * time.Second

// Question is a typed question. Type is "noul" or "choice". A noul's
// criteria, if any, describe "true" and "false"; a choice's map each option
// name to its description.
type Question struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria,omitempty"`
}

// Answer is the server's answer to one question.
type Answer struct {
	Type string `json:"type"`
	// Noul is P(true), for a noul.
	Noul *float64 `json:"noul,omitempty"`
	// Choice, Probabilities and Confidence are set for a choice.
	Choice        string             `json:"choice,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
}

// P returns P(true) for a noul.
func (a Answer) P() float64 {
	if a.Noul == nil {
		return 0
	}
	return *a.Noul
}

// Usage is the server's token count.
type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// Response is one /v1/systemone reply.
type Response struct {
	// Model is the name the server answers as: its --model-name, or the
	// checkpoint directory's basename.
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   Usage             `json:"usage"`
	// LatencyMS is the server's own wall time for the request.
	LatencyMS float64 `json:"latency_ms"`
	// Raw is the response body verbatim.
	Raw json.RawMessage `json:"-"`
	// Elapsed is the client's wall time for the request.
	Elapsed time.Duration `json:"-"`
}

// Health is the server's GET /health reply.
type Health struct {
	Status      string          `json:"status"`
	Model       string          `json:"model"`
	Checkpoint  string          `json:"checkpoint"`
	BaseModel   string          `json:"base_model"`
	MaxLength   int             `json:"max_length"`
	Device      string          `json:"device"`
	Temperature json.RawMessage `json:"temperature"`
}

// Client talks to one strands-decider server. By default it sends one
// request at a time: strands-decider aborts on concurrent requests on Apple
// Silicon, and releases before 930e6d5d also returned wrong probabilities
// under concurrency on CUDA. Concurrent lets a Linux CUDA server at the
// pinned commit batch requests; its equivalence with serial requests is
// checked before use (registration #33).
type Client struct {
	URL        string
	HTTP       *http.Client
	Health     Health
	Concurrent bool
	mu         sync.Mutex
}

// New connects and checks the server's health.
func New(ctx context.Context, url string) (*Client, error) {
	c := &Client{URL: strings.TrimRight(url, "/"), HTTP: &http.Client{Timeout: Timeout}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/health", nil)
	if err != nil {
		return nil, err
	}
	data, err := c.do(req)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &c.Health); err != nil {
		return nil, fmt.Errorf("strands-decider /health: %w", err)
	}
	// strands-decider and llama.cpp report "ok"; the official d3_server.py
	// reports "ready".
	if c.Health.Status != "ok" && c.Health.Status != "ready" {
		return nil, fmt.Errorf("strands-decider /health: status %q", c.Health.Status)
	}
	return c, nil
}

func (c *Client) do(req *http.Request) ([]byte, error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return data, fmt.Errorf("strands-decider %s: %s: %s", req.URL.Path, resp.Status, data)
	}
	return data, nil
}

// Ask asks every question about state in one request. Every question must
// come back answered with its own type; a noul's P(true) must lie in [0, 1].
// Raw is set whenever the server replied with JSON, even on error.
func (c *Client) Ask(ctx context.Context, state string, questions map[string]Question) (Response, error) {
	if len(questions) == 0 {
		return Response{}, fmt.Errorf("strands: no questions")
	}
	body, err := json.Marshal(map[string]any{"state": state, "model": RequestModel, "questions": questions})
	if err != nil {
		return Response{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/v1/systemone", bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	if !c.Concurrent {
		c.mu.Lock()
	}
	start := time.Now()
	data, err := c.do(req)
	elapsed := time.Since(start)
	if !c.Concurrent {
		c.mu.Unlock()
	}

	var r Response
	if json.Valid(data) {
		r.Raw = json.RawMessage(data)
	}
	r.Elapsed = elapsed
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, fmt.Errorf("strands-decider: %w", err)
	}
	for id, q := range questions {
		a, ok := r.Answers[id]
		switch {
		case !ok:
			return r, fmt.Errorf("strands-decider: no answer to %s", id)
		case a.Type != q.Type:
			return r, fmt.Errorf("strands-decider: %s answered as %s, asked as %s", id, a.Type, q.Type)
		case q.Type == "noul" && (a.Noul == nil || math.IsNaN(*a.Noul) || *a.Noul < 0 || *a.Noul > 1):
			return r, fmt.Errorf("strands-decider: %s: no P(true) in [0, 1]", id)
		case q.Type == "choice" && len(a.Probabilities) == 0:
			return r, fmt.Errorf("strands-decider: %s: no probabilities", id)
		}
	}
	return r, nil
}
