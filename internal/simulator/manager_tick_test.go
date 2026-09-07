package simulator

import (
	"testing"
	"time"
)

func TestClampSimulationDelta(t *testing.T) {
	tick := 2 * time.Second
	if got := clampSimulationDelta(0, tick); got != tick {
		t.Fatalf("zero dt: got %s", got)
	}
	if got := clampSimulationDelta(-time.Second, tick); got != tick {
		t.Fatalf("negative dt: got %s", got)
	}
	if got := clampSimulationDelta(5*time.Second, tick); got != 5*time.Second {
		t.Fatalf("normal dt: got %s", got)
	}
	if got := clampSimulationDelta(2*time.Hour, tick); got != maxSimulationDelta {
		t.Fatalf("clamped dt: got %s", got)
	}
}