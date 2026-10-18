package arms

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/scttfrdmn/ambit/internal/cedarq"
	"github.com/scttfrdmn/ambit/internal/corpus"
	"github.com/scttfrdmn/ambit/internal/serialize"
	"github.com/scttfrdmn/ambit/internal/strands"
)

const corpusDir = "../../corpus"

type systemOne struct {
	State     string                      `json:"state"`
	Model     string                      `json:"model"`
	Questions map[string]strands.Question `json:"questions"`
}

// fakeStrands answers every question with p(question id) and hands each
// request to check.
func fakeStrands(t *testing.T, p map[string]float64, check func(systemOne)) *strands.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Write([]byte(`{"status":"ok","model":"v21"}`))
			return
		}
		var req systemOne
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		check(req)
		if p == nil {
			w.WriteHeader(422)
			w.Write([]byte(`{"detail":"prompt exceeds window"}`))
			return
		}
		ans := map[string]any{}
		for id := range req.Questions {
			ans[id] = map[string]any{"type": "noul", "noul": p[id]}
		}
		b, _ := json.Marshal(map[string]any{"model": "v21", "answers": ans,
			"usage": map[string]int{"input_tokens": 900, "output_tokens": 1}, "latency_ms": 12.5})
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	c, err := strands.New(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func records(t *testing.T) []corpus.Record {
	t.Helper()
	recs, err := corpus.ReadJSONL[corpus.Record](corpusDir + "/" + corpus.RecordsFile)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func TestStrandsGate(t *testing.T) {
	r := records(t)[0]
	const policy = "POLICY PROSE"
	wantState, wantSHA := serialize.StrandsState(policy, r)
	for _, tc := range []struct {
		hook, shared float64
		allow        bool
	}{{0.45, 0.2, true}, {0.4499, 0.9, false}, {0.97, 0.6, true}} {
		c := fakeStrands(t, map[string]float64{StrandsHookID: tc.hook, StrandsSharedID: tc.shared}, func(req systemOne) {
			if req.State != wantState {
				t.Errorf("state differs from StrandsState")
			}
			got, _ := json.Marshal(req.Questions)
			want := `{"gate":{"type":"noul","instructions":"` + serialize.Question + `"},` +
				`"policy_allows":{"type":"noul","instructions":"Does the POLICY allow the PROPOSED TOOL CALL?",` +
				`"criteria":{"false":"a rule in the policy forbids this call","true":"every rule in the policy that applies to this call is satisfied"}}}`
			if string(got) != want {
				t.Errorf("questions\n got %s\nwant %s", got, want)
			}
		})
		g := &Strands{Arm: "a-strands", Policy: policy, C: c, Pin: strands.HobsonV21}
		d := g.Decide(context.Background(), r)
		if d.Error != "" || d.Allow != tc.allow || d.Refused {
			t.Errorf("hook %v: decision %+v", tc.hook, d)
		}
		if d.PAllow == nil || *d.PAllow != tc.hook || d.PShared == nil || *d.PShared != tc.shared {
			t.Errorf("hook %v: p_allow %v p_allow_shared %v", tc.hook, d.PAllow, d.PShared)
		}
		if d.StateSHA256 != wantSHA || d.ModelCalls != 1 || d.InputTokens != 900 || d.LatencyNS <= 0 ||
			d.ServedModel != strands.HobsonV21.ID() || d.CallDate == "" || len(d.Determining) != 0 {
			t.Errorf("hook %v: record %+v", tc.hook, d)
		}
		if len(d.Raw) != 1 || !json.Valid(d.Raw[0].Reply) {
			t.Errorf("hook %v: raw %+v", tc.hook, d.Raw)
		}
	}
}

func TestStrandsGateErrorDenies(t *testing.T) {
	c := fakeStrands(t, nil, func(systemOne) {})
	d := (&Strands{Arm: "a-strands", Policy: "P", C: c, Pin: strands.HobsonV21}).Decide(context.Background(), records(t)[0])
	if d.Allow || d.Error == "" || d.PAllow != nil || d.PShared != nil {
		t.Fatalf("decision %+v", d)
	}
	if len(d.Raw) != 1 || string(d.Raw[0].Reply) != `{"detail":"prompt exceeds window"}` {
		t.Fatalf("raw %+v", d.Raw)
	}
}

// TestStrandsPIP runs B-strands on the frozen corpus's margin records: the
// PIP sees only the payload and the Cedar arm's narrow question, with no
// criteria, its P(true) becomes the fact at 0.5, and the facts reach Cedar.
func TestStrandsPIP(t *testing.T) {
	pol, err := cedarq.LoadPolicies(corpusDir)
	if err != nil {
		t.Fatal(err)
	}
	var cur corpus.Record
	allow := map[float64]map[string]bool{0.9: {}, 0.1: {}}
	for _, p := range []float64{0.9, 0.1} {
		c := fakeStrands(t, map[string]float64{StrandsPIPID: p}, func(req systemOne) {
			q, ok := req.Questions[StrandsPIPID]
			if len(req.Questions) != 1 || !ok || q.Type != "noul" || q.Criteria != nil {
				t.Errorf("questions %+v", req.Questions)
			}
			if req.State != cur.Payload.Text {
				t.Errorf("%s: state is not the payload", cur.ID)
			}
			found := false
			for _, f := range []string{"sql_touches_identified", "payload_contains_identified", "doc_on_subject"} {
				if prop, ok := pipQuestion(f, cur); ok && prop == q.Instructions {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: instructions %q are not a PIP question", cur.ID, q.Instructions)
			}
		})
		pip := &StrandsPIP{C: c, Pin: strands.HobsonV21}
		g := &Cedar{Arm: "b-strands", Policies: pol, PIP: pip}
		asked := 0
		for _, r := range records(t) {
			cur = r
			d := g.Decide(context.Background(), r)
			if d.Error != "" {
				t.Fatalf("%s: %s", r.ID, d.Error)
			}
			if d.ModelCalls == 0 {
				continue
			}
			asked++
			if d.ServedModel != pip.ID() || d.InputTokens != 900*int64(d.ModelCalls) || len(d.Raw) != d.ModelCalls {
				t.Errorf("%s: record %+v", r.ID, d)
			}
			for f, fact := range d.Facts {
				if fact.P != p || fact.Value != (p >= 0.5) {
					t.Errorf("%s: fact %s = %+v", r.ID, f, fact)
				}
			}
			allow[p][r.ID] = d.Allow
		}
		if asked == 0 {
			t.Fatal("no margin record reached the PIP")
		}
		t.Logf("p=%.1f: %d records asked the PIP", p, asked)
	}
	flips := 0
	for id, a := range allow[0.9] {
		if a != allow[0.1][id] {
			flips++
		}
	}
	if flips == 0 {
		t.Fatal("the PIP's facts never changed a decision")
	}
}
