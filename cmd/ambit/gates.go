package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/scttfrdmn/ambit/internal/arms"
	"github.com/scttfrdmn/ambit/internal/bedrock"
	"github.com/scttfrdmn/ambit/internal/cedarq"
	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/hosted"
	"github.com/scttfrdmn/ambit/internal/jeb"
	"github.com/scttfrdmn/ambit/internal/serialize"
	"github.com/scttfrdmn/ambit/internal/strands"
)

// The per-run hosted cap (Law 6) is the -run-cap flag: $5 by default, and
// never above $15, the most Scott has approved for one run (the A-hosted
// N = 10,000 swarm, #43). The project or gate ceiling is the -ceiling flag,
// checked against the spend ledger in the results directory: keep one
// results directory per gate.
const (
	defaultRunCapDollars = 5
	maxRunCapDollars     = 15
)

// runCapMicros converts a -run-cap value to micro-dollars, refusing values
// at or below zero and above the approved maximum.
func runCapMicros(dollars float64) (int64, error) {
	if !(dollars > 0) || dollars > maxRunCapDollars {
		return 0, fmt.Errorf("-run-cap $%g: must be above $0 and at most $%d", dollars, maxRunCapDollars)
	}
	return int64(math.Round(dollars * 1e6)), nil
}

// ablations are the registered Arm A prose ablations.
var ablations = map[string]func(string) (string, error){
	"failclosed": serialize.WithoutFailClosed,
}

// armsHelp lists the arms ambit run and ambit swarm accept.
const armsHelp = "b0 | b | b-4b | b-hosted | b-strands | b-safeguard | a-local | a-hosted | a-strands | a-d3 | a-safeguard"

// gateFlags choose an arm and reach its models. ambit run and ambit swarm
// share them, so both build an arm's gate with the same code and the same
// configuration (Law 3).
type gateFlags struct {
	arm, ablation, name, llama, strandsURL *string
	bedrockRegion, d3URL, d3Runtime        *string
	cachePrompt, strandsConcurrent         *bool
	ceiling, runCap                        *float64
}

func addGateFlags(fs *flag.FlagSet) *gateFlags {
	return &gateFlags{
		arm:               fs.String("arm", "b0", armsHelp),
		cachePrompt:       fs.Bool("cache-prompt", false, "let llama-server reuse cached prompt prefixes"),
		ablation:          fs.String("ablation", "", "Arm A prose ablation: failclosed"),
		name:              fs.String("name", "", "arm and run name (default: the arm, plus -ablation if any)"),
		llama:             fs.String("llama", "http://127.0.0.1:8080", "llama-server serving the pinned Jebadiah GGUF"),
		runCap:            fs.Float64("run-cap", defaultRunCapDollars, "per-run hosted spend cap in dollars (at most 15; $5 unless Scott approved more for the run)"),
		ceiling:           fs.Float64("ceiling", 200, "hosted spend ceiling in dollars for this results directory's ledger (G1 $200, G2 $300, G3 $500)"),
		bedrockRegion:     fs.String("bedrock-region", "us-east-1", "Bedrock region for gpt-oss-safeguard (in-Region only)"),
		d3URL:             fs.String("d3", "http://127.0.0.1:8090", "systemone server for d3 (llama.cpp Q8_0, or the official d3_server.py)"),
		d3Runtime:         fs.String("d3-runtime", "q8", "which d3 is behind -d3: q8 (llama.cpp Q8_0) or official (BF16 d3_server.py)"),
		strandsConcurrent: fs.Bool("strands-concurrent", false, "send strands-decider requests concurrently (Linux CUDA only; see internal/strands)"),
		strandsURL:        fs.String("strands", "http://127.0.0.1:8000", "strands-decider serve, with the pinned hobson-v21 checkpoint"),
	}
}

// isA reports whether the arm is an Arm A (decision model) gate.
func (gf *gateFlags) isA() bool {
	switch *gf.arm {
	case "a-local", "a-hosted", "a-strands", "a-d3", "a-safeguard":
		return true
	}
	return false
}

// armName is the arm's name in results: the arm, plus -ablation, unless
// -name overrides it.
func (gf *gateFlags) armName() string {
	if *gf.name != "" {
		return *gf.name
	}
	if *gf.ablation != "" {
		return *gf.arm + "-ablation"
	}
	return *gf.arm
}

// builtGate is a gate with what a manifest records about it.
type builtGate struct {
	gate        arms.Gate
	models      map[string]string
	cachePrompt bool
	proseSHA256 string
	// spent reports the run's hosted spend so far, if the arm is hosted.
	spent func() int64
	// runCapMicros is the per-run hosted cap the gate was built with.
	runCapMicros int64
}

// newSafeguard reaches Bedrock's gpt-oss-safeguard; tests replace it.
var newSafeguard = func(ctx context.Context, region string) (arms.SafeguardAsker, error) {
	c, err := bedrock.New(ctx, region)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// build constructs the arm's gate over the policies in corpusDir. A hosted
// arm's spend ledger is results/spend.jsonl, with the per-run cap and the
// -ceiling.
func (gf *gateFlags) build(ctx context.Context, corpusDir, results string) (*builtGate, error) {
	runCap, err := runCapMicros(*gf.runCap)
	if err != nil {
		return nil, err
	}
	pol, err := cedarq.LoadPolicies(corpusDir)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(corpusDir, corpus.PolicyDir, corpus.ProseFile))
	if err != nil {
		return nil, err
	}
	prose := string(b)
	if *gf.ablation != "" {
		ablate, ok := ablations[*gf.ablation]
		if !ok {
			return nil, fmt.Errorf("unknown ablation %q", *gf.ablation)
		}
		if !gf.isA() {
			return nil, fmt.Errorf("ablation %s applies to Arm A only", *gf.ablation)
		}
		if prose, err = ablate(prose); err != nil {
			return nil, err
		}
	}
	armName := gf.armName()
	out := &builtGate{models: map[string]string{}, runCapMicros: runCap}
	if gf.isA() {
		h := sha256.Sum256([]byte(prose))
		out.proseSHA256 = hex.EncodeToString(h[:])
	}

	local := func() (*arms.Jeb, error) {
		c, err := jeb.New(ctx, *gf.llama)
		if err != nil {
			return nil, fmt.Errorf("llama-server at %s: %w (see README: Running the local model)", *gf.llama, err)
		}
		c.CachePrompt = *gf.cachePrompt
		out.cachePrompt = *gf.cachePrompt
		j := &arms.Jeb{C: c, Pin: jeb.Jebadiah4BV2}
		out.models["jebadiah"] = j.ID() + " sha256:" + jeb.Jebadiah4BV2.FileSHA256
		return j, nil
	}
	decider := func() (*strands.Client, error) {
		c, err := strands.New(ctx, *gf.strandsURL)
		if err != nil {
			return nil, fmt.Errorf("strands-decider at %s: %w", *gf.strandsURL, err)
		}
		if *gf.strandsConcurrent && c.Health.Device != "cuda" {
			return nil, fmt.Errorf("-strands-concurrent needs a CUDA server; this one is on %q", c.Health.Device)
		}
		c.Concurrent = *gf.strandsConcurrent
		// The pin, and what the server itself says it loaded.
		out.models["strands"] = strands.HobsonV21.ID() + " base " + strands.HobsonV21.BaseRepo + "@" + strands.HobsonV21.BaseRevision +
			" server " + strands.HobsonV21.Server
		out.models["strands_server_reports"] = fmt.Sprintf("model %s checkpoint %s base %s device %s",
			c.Health.Model, c.Health.Checkpoint, c.Health.BaseModel, c.Health.Device)
		return c, nil
	}
	remote := func() (*arms.Hosted, error) {
		l, err := hosted.OpenLedger(filepath.Join(results, "spend.jsonl"), armName, runCap, int64(*gf.ceiling*1e6))
		if err != nil {
			return nil, err
		}
		c, err := hosted.FromEnv(l)
		if err != nil {
			return nil, err
		}
		h := &arms.Hosted{C: c}
		out.models["hosted"] = h.ID()
		out.spent = l.RunSpent
		return h, nil
	}

	switch *gf.arm {
	case "b0":
		out.gate = &arms.Cedar{Arm: armName, Policies: pol}
	case "b", "b-4b":
		// b is G1's name for Cedar with the local Jebadiah PIP; b-4b names
		// the same gate beside other PIP sizes.
		j, err := local()
		if err != nil {
			return nil, err
		}
		out.gate = &arms.Cedar{Arm: armName, Policies: pol, PIP: j}
	case "b-hosted":
		h, err := remote()
		if err != nil {
			return nil, err
		}
		out.gate = &arms.Cedar{Arm: armName, Policies: pol, PIP: h}
	case "a-local":
		j, err := local()
		if err != nil {
			return nil, err
		}
		out.gate = &arms.Model{Arm: armName, Policy: prose, M: j}
	case "a-hosted":
		h, err := remote()
		if err != nil {
			return nil, err
		}
		out.gate = &arms.Model{Arm: armName, Policy: prose, M: h}
	case "a-strands":
		c, err := decider()
		if err != nil {
			return nil, err
		}
		out.gate = &arms.Strands{Arm: armName, Policy: prose, C: c, Pin: strands.HobsonV21}
	case "a-d3":
		pin := strands.D3Q8
		switch *gf.d3Runtime {
		case "q8":
		case "official":
			pin = strands.D3Official
		default:
			return nil, fmt.Errorf("unknown -d3-runtime %q", *gf.d3Runtime)
		}
		c, err := strands.New(ctx, *gf.d3URL)
		if err != nil {
			return nil, fmt.Errorf("d3 at %s: %w", *gf.d3URL, err)
		}
		out.models["d3"] = pin.ID() + " base " + pin.BaseRepo + "@" + pin.BaseRevision + " server " + pin.Server
		// d3 gets the shared state and gate proposition as a noul, like
		// A-local and A-hosted; no wrapper instructions (registration #33).
		out.gate = &arms.Model{Arm: armName, Policy: prose, M: &arms.StrandsPIP{C: c, Pin: pin}}
	case "a-safeguard", "b-safeguard":
		c, err := newSafeguard(ctx, *gf.bedrockRegion)
		if err != nil {
			return nil, err
		}
		s := &arms.Safeguard{C: c, Gate: *gf.arm == "a-safeguard"}
		out.models["safeguard"] = s.ID() + " region " + *gf.bedrockRegion + "; cost axis at list price (internal account)"
		if s.Gate {
			out.gate = &arms.Model{Arm: armName, Policy: prose, M: s}
		} else {
			out.gate = &arms.Cedar{Arm: armName, Policies: pol, PIP: s}
		}
	case "b-strands":
		c, err := decider()
		if err != nil {
			return nil, err
		}
		out.gate = &arms.Cedar{Arm: armName, Policies: pol, PIP: &arms.StrandsPIP{C: c, Pin: strands.HobsonV21}}
	default:
		return nil, fmt.Errorf("unknown arm %q", *gf.arm)
	}
	return out, nil
}
