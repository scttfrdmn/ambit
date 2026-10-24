package swarm

import "testing"

// TestRegisteredScripts checks the G3 registration (#21): with the
// registered seed, K = 4 and the default shape, the scripted gate calls,
// probes' work calls included, are 48 / 496 / 5,229 / 52,119.
func TestRegisteredScripts(t *testing.T) {
	want := map[int]int{10: 48, 100: 496, 1000: 5229, 10000: 52119}
	for _, n := range []int{10, 100, 1000, 10000} {
		s, err := NewScript(Default(RegisteredSeed, n, RegisteredCalls))
		if err != nil {
			t.Fatal(err)
		}
		got := 0
		for _, a := range s.Agents {
			got += len(a.Work) + len(a.Delegations)
		}
		if got != want[n] {
			t.Errorf("N=%d: %d scripted gate calls, registered %d", n, got, want[n])
		}
	}
	cfg := Default(RegisteredSeed, 1000, RegisteredCalls)
	if cfg.MaxTaskAgents != 15 || cfg.ProbePercent != 20 || cfg.SwarmCapPercent != 80 || cfg.TaskCapScale != 100 {
		t.Errorf("default shape %+v differs from the registration", cfg)
	}
}
