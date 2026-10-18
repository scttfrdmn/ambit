package hosted

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Ledger enforces the spend caps (Law 6): a per-run cap and a project
// ceiling. Before a call, Reserve checks the estimated cost against both;
// after it, Settle replaces the estimate with the billed cost and appends a
// line to the spend log. The project total is the sum of the log.
type Ledger struct {
	mu         sync.Mutex
	path       string
	run        string
	runCap     int64
	projectCap int64
	runSpent   int64
	priorSpent int64
	pending    int64
	Calls      int64
	// lock is held open for the process's lifetime (see OpenLedger).
	lock        *os.File
	InputTokens int64
}

// Entry is one line of the spend log.
type Entry struct {
	Time        string `json:"time"`
	Run         string `json:"run"`
	Model       string `json:"model"`
	ServedModel string `json:"served_model"`
	InputTokens int64  `json:"input_tokens"`
	CostMicros  int64  `json:"cost_micros"`
	Record      string `json:"record"`
	// Attempts counts HTTP attempts, retries included (G2); absent in G1 logs.
	Attempts int `json:"attempts,omitempty"`
}

// OpenLedger reads the spend log at path (which may not exist yet) and
// returns a ledger for one run.
//
// The ledger holds an exclusive lock on <path>.lock for the life of the
// process, so a second hosted run against the same ledger fails at once
// instead of spending past a ceiling it cannot see.
func OpenLedger(path, run string, runCapMicros, projectCapMicros int64) (*Ledger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	lf, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lf.Close()
		return nil, fmt.Errorf("spend ledger %s is in use by another hosted run; run hosted shards one at a time", path)
	}
	l := &Ledger{path: path, run: run, runCap: runCapMicros, projectCap: projectCapMicros, lock: lf}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	for dec.More() {
		var e Entry
		if err := dec.Decode(&e); err != nil {
			return nil, fmt.Errorf("spend log %s: %w", path, err)
		}
		l.priorSpent += e.CostMicros
	}
	if l.priorSpent >= projectCapMicros {
		return nil, fmt.Errorf("project ceiling reached: $%.4f spent of $%.2f", float64(l.priorSpent)/1e6, float64(projectCapMicros)/1e6)
	}
	return l, nil
}

// Reserve holds an estimated cost, or refuses if it would cross a cap.
// Reservations are summed, so concurrent calls together never cross a cap:
// each is checked against the billed spend plus every estimate still
// pending. Every successful Reserve must be followed by one Settle or
// Release with the same estimate.
func (l *Ledger) Reserve(est int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.runSpent+l.pending+est > l.runCap {
		return fmt.Errorf("per-run cap: $%.4f spent, $%.4f pending, next call ~$%.6f, cap $%.2f",
			float64(l.runSpent)/1e6, float64(l.pending)/1e6, float64(est)/1e6, float64(l.runCap)/1e6)
	}
	if l.priorSpent+l.runSpent+l.pending+est > l.projectCap {
		return fmt.Errorf("project ceiling: $%.4f spent, $%.4f pending, cap $%.2f",
			float64(l.priorSpent+l.runSpent)/1e6, float64(l.pending)/1e6, float64(l.projectCap)/1e6)
	}
	l.pending += est
	return nil
}

// Release drops a reservation of est for a call that was not billed.
func (l *Ledger) Release(est int64) {
	l.mu.Lock()
	l.pending -= est
	l.mu.Unlock()
}

// Settle replaces a reservation of est with the call's billed cost.
func (l *Ledger) Settle(est, cost int64) {
	l.mu.Lock()
	l.pending -= est
	l.runSpent += cost
	l.mu.Unlock()
}

// Log appends a spend entry.
func (l *Ledger) Log(a Answer, record string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Calls++
	l.InputTokens += a.InputTokens
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(Entry{
		Time: time.Now().UTC().Format(time.RFC3339), Run: l.run, Model: Model, ServedModel: a.ServedModel,
		InputTokens: a.InputTokens, CostMicros: a.CostMicros, Record: record, Attempts: a.Attempts,
	})
}

// RunSpent returns the run's billed spend in micro-dollars.
func (l *Ledger) RunSpent() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.runSpent
}

// Close releases the ledger's lock.
func (l *Ledger) Close() error {
	if l.lock == nil {
		return nil
	}
	err := l.lock.Close()
	l.lock = nil
	return err
}
