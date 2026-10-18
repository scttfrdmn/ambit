package arms

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/scttfrdmn/ambit/internal/hosted"
	"github.com/scttfrdmn/ambit/internal/jeb"
)

// Jeb asks Jebadiah a noul. The proposition is the question's instructions;
// the options are true and false with no descriptions.
type Jeb struct {
	C   *jeb.Client
	Pin jeb.Pin
}

// ID names the model and its pin.
func (j *Jeb) ID() string { return j.Pin.Repo + "@" + j.Pin.Revision + "/" + j.Pin.File }

// Ask answers P(true).
func (j *Jeb) Ask(ctx context.Context, state, prop string) (Reply, error) {
	a, err := j.C.Ask(ctx, state, jeb.Question{Type: "noul", Instructions: prop})
	if err != nil {
		return Reply{}, err
	}
	h := sha256.Sum256([]byte(a.Prompt))
	raw, _ := json.Marshal(map[string]any{
		"prompt_sha256": hex.EncodeToString(h[:]), "label_logprobs": a.Logprobs,
		"labels_missing": a.Missing, "probs": a.Probs, "temperature": jeb.Temperatures["noul"],
	})
	return Reply{P: a.P(), ServedModel: j.ID(), Date: time.Now().UTC().Format("2006-01-02"), Raw: raw}, nil
}

// Hosted asks OpenAI Decisions a predicate and logs the spend.
type Hosted struct {
	C *hosted.Client
	// Record names the record being decided, for the spend log.
	Record string
}

// ID names the requested model.
func (h *Hosted) ID() string { return "openai/decisions/" + hosted.Model }

// Ask answers P(true).
func (h *Hosted) Ask(ctx context.Context, state, prop string) (Reply, error) {
	a, err := h.C.Ask(ctx, state, prop)
	rep := Reply{P: a.P, Refused: a.Refused, ServedModel: a.ServedModel, Date: a.Date,
		InputTokens: a.InputTokens, CostMicros: a.CostMicros, Raw: a.Raw}
	if a.ServedModel != "" {
		if lerr := h.C.Ledger.Log(a, h.Record); lerr != nil && err == nil {
			err = lerr
		}
	}
	return rep, err
}
