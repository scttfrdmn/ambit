package strands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const health = `{"status":"ok","model":"v21","checkpoint":"v21","base_model":"Qwen/Qwen3.5-2B-Base","num_slots":24,"max_length":4096,"temperature":{"noul":0.8177},"device":"cpu","prefix_cache":true,"vision":false}`

// fake serves /health and answers /v1/systemone with reply, recording the
// most requests in flight at once.
func fake(t *testing.T, reply func(req map[string]any) (int, string)) (*httptest.Server, *atomic.Int32) {
	var inflight, most atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.Write([]byte(health))
		case "/v1/systemone":
			n := inflight.Add(1)
			defer inflight.Add(-1)
			for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
			}
			time.Sleep(5 * time.Millisecond)
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			code, body := reply(req)
			w.WriteHeader(code)
			w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &most
}

func TestAsk(t *testing.T) {
	const body = `{"model":"v21","answers":{"a":{"type":"noul","noul":0.8581},"c":{"type":"choice","choice":"billing","probabilities":{"billing":0.77,"sales":0.23},"confidence":0.54}},"usage":{"input_tokens":86,"output_tokens":1},"latency_ms":489.01}`
	srv, _ := fake(t, func(req map[string]any) (int, string) {
		want := `{"model":"strands-decider-latest","questions":{"a":{"criteria":{"false":"no","true":"yes"},"instructions":"Is it?","type":"noul"},"c":{"criteria":{"billing":"an invoice","sales":"a quote"},"instructions":"Which team?","type":"choice"}},"state":"S"}`
		if got, _ := json.Marshal(req); string(got) != want {
			t.Errorf("request\n got %s\nwant %s", got, want)
		}
		return 200, body
	})
	c, err := New(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if c.Health.Model != "v21" || c.Health.MaxLength != 4096 || c.Health.Device != "cpu" {
		t.Errorf("health %+v", c.Health)
	}
	r, err := c.Ask(context.Background(), "S", map[string]Question{
		"a": {Type: "noul", Instructions: "Is it?", Criteria: map[string]string{"true": "yes", "false": "no"}},
		"c": {Type: "choice", Instructions: "Which team?", Criteria: map[string]string{"billing": "an invoice", "sales": "a quote"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Answers["a"].P() != 0.8581 || r.Model != "v21" || r.Usage.InputTokens != 86 || r.LatencyMS != 489.01 {
		t.Errorf("response %+v", r)
	}
	if ch := r.Answers["c"]; ch.Choice != "billing" || ch.Probabilities["sales"] != 0.23 || ch.Confidence != 0.54 {
		t.Errorf("choice %+v", ch)
	}
	if string(r.Raw) != body {
		t.Errorf("raw %s", r.Raw)
	}
}

func TestNoCriteriaOmitted(t *testing.T) {
	srv, _ := fake(t, func(req map[string]any) (int, string) {
		q := req["questions"].(map[string]any)["g"].(map[string]any)
		if _, ok := q["criteria"]; ok {
			t.Errorf("criteria sent: %v", q)
		}
		return 200, `{"model":"m","answers":{"g":{"type":"noul","noul":0.1}},"usage":{"input_tokens":3,"output_tokens":1},"latency_ms":1}`
	})
	c, err := New(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Ask(context.Background(), "S", map[string]Question{"g": {Type: "noul", Instructions: "q"}}); err != nil {
		t.Fatal(err)
	}
}

func TestOneAtATime(t *testing.T) {
	srv, most := fake(t, func(map[string]any) (int, string) {
		return 200, `{"model":"m","answers":{"g":{"type":"noul","noul":0.5}},"usage":{"input_tokens":1,"output_tokens":1},"latency_ms":1}`
	})
	c, err := New(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Ask(context.Background(), "S", map[string]Question{"g": {Type: "noul", Instructions: "q"}}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if most.Load() != 1 {
		t.Fatalf("%d requests in flight at once", most.Load())
	}
}

func TestBadReplies(t *testing.T) {
	q := map[string]Question{"g": {Type: "noul", Instructions: "q"}}
	for name, tc := range map[string]struct {
		code    int
		body    string
		wantErr string
		raw     bool
	}{
		"422":        {422, `{"detail":"prompt too long"}`, "422", true},
		"not json":   {500, `Internal Server Error`, "500", false},
		"missing":    {200, `{"model":"m","answers":{},"usage":{}}`, "no answer to g", true},
		"wrong type": {200, `{"model":"m","answers":{"g":{"type":"choice","choice":"x","probabilities":{"x":1},"confidence":1}},"usage":{}}`, "answered as choice", true},
		"no noul":    {200, `{"model":"m","answers":{"g":{"type":"noul"}},"usage":{}}`, "no P(true)", true},
		"over one":   {200, `{"model":"m","answers":{"g":{"type":"noul","noul":1.5}},"usage":{}}`, "no P(true)", true},
	} {
		srv, _ := fake(t, func(map[string]any) (int, string) { return tc.code, tc.body })
		c, err := New(context.Background(), srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		r, err := c.Ask(context.Background(), "S", q)
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: err %v, want %q", name, err, tc.wantErr)
		}
		if (r.Raw != nil) != tc.raw {
			t.Errorf("%s: raw %q", name, r.Raw)
		}
	}
}

func TestHealthFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"loading"}`))
	}))
	defer srv.Close()
	if _, err := New(context.Background(), srv.URL); err == nil {
		t.Fatal("unhealthy server accepted")
	}
	srv.Close()
	if _, err := New(context.Background(), srv.URL); err == nil {
		t.Fatal("dead server accepted")
	}
}
