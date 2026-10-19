// Package bedrock asks openai.gpt-oss-safeguard-120b on Amazon Bedrock a
// yes/no question, for Arm A-safeguard (a policy-reading fused gate) and Arm
// B-safeguard (the same model as a PIP). Registration #33, items 12–13.
//
// The model is a reasoning model that reads a policy and classifies against
// it. It is called with InvokeModel and an OpenAI chat-completions body at
// reasoning effort low and temperature 0. Its answer token follows the
// reasoning, so its log probability is saturated and the arm is a hard
// label: one operating point, not a curve. Reasoning cannot be switched off.
//
// The account is internal, so Bedrock calls cost nothing out of pocket. Each
// answer still records its tokens and the list-price cost, which is what the
// report's cost axis uses.
package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
)

// Model is the Bedrock model ID (no inference profile; in-Region only).
const Model = "openai.gpt-oss-safeguard-120b"

// List prices, us-east-1 on demand (AWS Price List API, 2026-10-18), in
// micro-dollars per million tokens.
const (
	InputMicrosPerMTok  = 150_000
	OutputMicrosPerMTok = 600_000
)

// ReasoningEffort is the registered effort. Medium is a pre-registered
// ablation, not a tuning knob.
const ReasoningEffort = "low"

// Answer is one reply.
type Answer struct {
	// Word is the final answer with surrounding whitespace removed.
	Word         string
	Refused      bool
	FinishReason string
	ServedModel  string
	Region       string
	Date         string
	InputTokens  int64
	OutputTokens int64
	CostMicros   int64 // at list price
	Reasoning    string
	Raw          json.RawMessage
	Elapsed      time.Duration
}

// Client calls InvokeModel.
type Client struct {
	rt     *bedrockruntime.Client
	region string
}

// New builds a client from the default AWS credential chain.
func New(ctx context.Context, region string) (*Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region),
		config.WithRetryer(func() aws.Retryer {
			// Throttling and 5xx are retried with backoff; the model's tail
			// latency reaches about 15 s, so allow a minute per call.
			return retry.AddWithMaxAttempts(retry.NewStandard(), 10)
		}))
	if err != nil {
		return nil, err
	}
	return &Client{rt: bedrockruntime.NewFromConfig(cfg), region: region}, nil
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type request struct {
	Model               string    `json:"model"`
	Messages            []message `json:"messages"`
	MaxCompletionTokens int       `json:"max_completion_tokens"`
	Temperature         float64   `json:"temperature"`
	ReasoningEffort     string    `json:"reasoning_effort"`
}

type response struct {
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content string  `json:"content"`
			Refusal *string `json:"refusal"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

var reasoningRE = regexp.MustCompile(`(?s)^\s*<reasoning>(.*?)</reasoning>`)

// Ask sends system and user messages and returns the final answer.
func (c *Client) Ask(ctx context.Context, system, user string) (Answer, error) {
	body, err := json.Marshal(request{
		Model: Model, MaxCompletionTokens: 2048, Temperature: 0, ReasoningEffort: ReasoningEffort,
		Messages: []message{{Role: "system", Content: system}, {Role: "user", Content: user}},
	})
	if err != nil {
		return Answer{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	start := time.Now()
	out, err := c.rt.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId: aws.String(Model), Body: body,
		ContentType: aws.String("application/json"), Accept: aws.String("application/json"),
	})
	elapsed := time.Since(start)
	a := Answer{Region: c.region, Date: start.UTC().Format("2006-01-02"), Elapsed: elapsed}
	if err != nil {
		return a, fmt.Errorf("bedrock: %w", err)
	}
	a.Raw = out.Body
	var r response
	if err := json.Unmarshal(out.Body, &r); err != nil {
		return a, fmt.Errorf("bedrock: decode: %w", err)
	}
	a.ServedModel = r.Model
	a.InputTokens, a.OutputTokens = r.Usage.PromptTokens, r.Usage.CompletionTokens
	a.CostMicros = (a.InputTokens*InputMicrosPerMTok + a.OutputTokens*OutputMicrosPerMTok + 999_999) / 1_000_000
	if len(r.Choices) != 1 {
		return a, fmt.Errorf("bedrock: %d choices", len(r.Choices))
	}
	ch := r.Choices[0]
	a.FinishReason = ch.FinishReason
	text := ch.Message.Content
	if m := reasoningRE.FindStringSubmatch(text); m != nil {
		a.Reasoning, text = m[1], text[len(m[0]):]
	}
	a.Word = strings.TrimSpace(text)
	a.Refused = ch.Message.Refusal != nil && *ch.Message.Refusal != ""
	return a, nil
}
