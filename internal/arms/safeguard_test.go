package arms

import (
	"context"
	"strings"
	"testing"

	"github.com/scttfrdmn/ambit/internal/bedrock"
)

type fakeSafeguard struct {
	a            bedrock.Answer
	system, user string
}

func (f *fakeSafeguard) Ask(_ context.Context, system, user string) (bedrock.Answer, error) {
	f.system, f.user = system, user
	return f.a, nil
}

func TestSafeguardParse(t *testing.T) {
	for _, c := range []struct {
		gate    bool
		word    string
		finish  string
		refusal bool
		p       float64
		refused bool
	}{
		{true, "ALLOWED", "stop", false, 1, false},
		{true, "DENIED", "stop", false, 0, false},
		{true, "allowed", "stop", false, 0, false}, // only exactly ALLOWED allows
		{true, "", "length", false, 0, true},
		{true, "ALLOWED", "stop", true, 0, true},
		{false, "YES", "stop", false, 1, false},
		{false, "NO", "stop", false, 0, false},
		{false, "MAYBE", "stop", false, 0, true}, // no fact supplied
	} {
		f := &fakeSafeguard{a: bedrock.Answer{Word: c.word, FinishReason: c.finish, Refused: c.refusal}}
		r, err := (&Safeguard{C: f, Gate: c.gate}).Ask(context.Background(), "STATE", "PROP")
		if err != nil {
			t.Fatal(err)
		}
		if r.P != c.p || r.Refused != c.refused {
			t.Errorf("gate=%v word=%q finish=%s refusal=%v: got P=%v refused=%v", c.gate, c.word, c.finish, c.refusal, r.P, r.Refused)
		}
		if c.gate && (f.system != SafeguardGateSystem || !strings.HasPrefix(f.user, "STATE\n\nQUESTION: PROP")) {
			t.Errorf("gate layout: system %q user %q", f.system, f.user)
		}
		if !c.gate && (f.system != SafeguardPIPSystem || !strings.HasPrefix(f.user, "PAYLOAD:\nSTATE")) {
			t.Errorf("pip layout: user %q", f.user)
		}
	}
}
