package model

import (
	"testing"
	"time"
)

func TestIsOperatingContinuous(t *testing.T) {
	now := time.Date(2026, 8, 25, 23, 0, 0, 0, time.UTC)
	if !IsOperating(now, OperatingProfile{Mode: OperatingModeContinuous}) {
		t.Fatal("continuous should operate at night")
	}
}

func TestIsOperatingShiftWeekdayWindow(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tashkent")
	if err != nil {
		t.Fatal(err)
	}
	profile := DefaultVehicleShiftProfile()
	inside := time.Date(2026, 8, 25, 10, 0, 0, 0, loc) // Tuesday
	outside := time.Date(2026, 8, 25, 20, 0, 0, 0, loc)
	sunday := time.Date(2026, 8, 23, 10, 0, 0, 0, loc)
	if !IsOperating(inside, profile) {
		t.Fatal("expected operating Tuesday 10:00")
	}
	if IsOperating(outside, profile) {
		t.Fatal("expected idle Tuesday 20:00")
	}
	if IsOperating(sunday, profile) {
		t.Fatal("expected idle Sunday")
	}
}

func TestIsOperatingOutOfServiceAndStoppedUntil(t *testing.T) {
	now := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	until := now.Add(24 * time.Hour)
	if IsOperating(now, OperatingProfile{Mode: OperatingModeOutOfService}) {
		t.Fatal("out of service should be idle")
	}
	if IsOperating(now, OperatingProfile{Mode: OperatingModeContinuous, StoppedUntil: &until}) {
		t.Fatal("stoppedUntil should freeze counters")
	}
	if !IsOperating(until.Add(time.Minute), OperatingProfile{Mode: OperatingModeContinuous, StoppedUntil: &until}) {
		t.Fatal("after stoppedUntil should run")
	}
}

func TestCounterDeltaRealtime(t *testing.T) {
	dt := 2 * time.Second
	hours := CounterDelta(MetricDefinition{Kind: "COUNTER", Unit: "h"}, OperatingProfile{}, dt)
	km := CounterDelta(MetricDefinition{Kind: "COUNTER", Unit: "km"}, OperatingProfile{}, dt)
	wantHours := dt.Hours()
	wantKm := dt.Hours() * DefaultAvgSpeedKmh
	if hours != wantHours {
		t.Fatalf("hours=%v want %v", hours, wantHours)
	}
	if km != wantKm {
		t.Fatalf("km=%v want %v", km, wantKm)
	}
}
