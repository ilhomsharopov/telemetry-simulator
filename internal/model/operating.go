package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	OperatingModeContinuous    = "CONTINUOUS"
	OperatingModeShift         = "SHIFT"
	OperatingModeOutOfService  = "OUT_OF_SERVICE"
	DefaultTimezone            = "Asia/Tashkent"
	DefaultAvgSpeedKmh         = 40.0
	DefaultRatedKw             = 10.0
	DefaultCyclesPerHour       = 60.0
	DefaultTonsPerHour         = 1.0
)

type OperatingProfile struct {
	Mode          string     `json:"mode"`
	Timezone      string     `json:"timezone,omitempty"`
	StartHour     int        `json:"startHour,omitempty"`
	HoursPerDay   float64    `json:"hoursPerDay,omitempty"`
	DaysOfWeek    []int      `json:"daysOfWeek,omitempty"`
	StoppedUntil  *time.Time `json:"stoppedUntil,omitempty"`
	AvgSpeedKmh   float64    `json:"avgSpeedKmh,omitempty"`
	RatedKw       float64    `json:"ratedKw,omitempty"`
	CyclesPerHour float64    `json:"cyclesPerHour,omitempty"`
	TonsPerHour   float64    `json:"tonsPerHour,omitempty"`
}

func (p OperatingProfile) Value() (driver.Value, error) {
	normalized := NormalizeOperatingProfile(p)
	return json.Marshal(normalized)
}

func (p *OperatingProfile) Scan(value any) error {
	if value == nil {
		*p = NormalizeOperatingProfile(OperatingProfile{})
		return nil
	}
	var raw []byte
	switch v := value.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("scan operating profile: unsupported type %T", value)
	}
	if len(raw) == 0 || string(raw) == "{}" {
		*p = NormalizeOperatingProfile(OperatingProfile{})
		return nil
	}
	var parsed OperatingProfile
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return err
	}
	*p = NormalizeOperatingProfile(parsed)
	return nil
}

func DefaultVehicleShiftProfile() OperatingProfile {
	return NormalizeOperatingProfile(OperatingProfile{
		Mode:        OperatingModeShift,
		Timezone:    DefaultTimezone,
		StartHour:   8,
		HoursPerDay: 8,
		DaysOfWeek:  []int{1, 2, 3, 4, 5, 6},
	})
}

func DefaultContinuousProfile() OperatingProfile {
	return NormalizeOperatingProfile(OperatingProfile{Mode: OperatingModeContinuous})
}

func NormalizeOperatingProfile(p OperatingProfile) OperatingProfile {
	mode := strings.ToUpper(strings.TrimSpace(p.Mode))
	if mode == "" {
		mode = OperatingModeContinuous
	}
	p.Mode = mode
	if strings.TrimSpace(p.Timezone) == "" {
		p.Timezone = DefaultTimezone
	}
	if p.StartHour < 0 || p.StartHour > 23 {
		p.StartHour = 8
	}
	if p.HoursPerDay <= 0 {
		p.HoursPerDay = 8
	}
	if p.HoursPerDay > 24 {
		p.HoursPerDay = 24
	}
	if len(p.DaysOfWeek) == 0 {
		p.DaysOfWeek = []int{1, 2, 3, 4, 5, 6}
	}
	if p.AvgSpeedKmh <= 0 {
		p.AvgSpeedKmh = DefaultAvgSpeedKmh
	}
	if p.RatedKw <= 0 {
		p.RatedKw = DefaultRatedKw
	}
	if p.CyclesPerHour <= 0 {
		p.CyclesPerHour = DefaultCyclesPerHour
	}
	if p.TonsPerHour <= 0 {
		p.TonsPerHour = DefaultTonsPerHour
	}
	return p
}

func IsOperating(now time.Time, profile OperatingProfile) bool {
	profile = NormalizeOperatingProfile(profile)
	if profile.StoppedUntil != nil && now.Before(*profile.StoppedUntil) {
		return false
	}
	switch profile.Mode {
	case OperatingModeOutOfService:
		return false
	case OperatingModeShift:
		return isInShiftWindow(now, profile)
	default:
		return true
	}
}

func isInShiftWindow(now time.Time, profile OperatingProfile) bool {
	loc, err := time.LoadLocation(profile.Timezone)
	if err != nil {
		loc = time.FixedZone("Tashkent", 5*60*60)
	}
	local := now.In(loc)
	if !isoDayAllowed(isoWeekday(local), profile.DaysOfWeek) {
		return false
	}
	if profile.HoursPerDay >= 24 {
		return true
	}
	minutes := local.Hour()*60 + local.Minute()
	startMin := profile.StartHour * 60
	endMin := startMin + int(profile.HoursPerDay*60)
	if endMin <= 24*60 {
		return minutes >= startMin && minutes < endMin
	}
	endMin -= 24 * 60
	return minutes >= startMin || minutes < endMin
}

func isoWeekday(t time.Time) int {
	day := int(t.Weekday())
	if day == 0 {
		return 7
	}
	return day
}

func isoDayAllowed(day int, days []int) bool {
	for _, candidate := range days {
		if candidate == day {
			return true
		}
	}
	return false
}

func CounterDelta(definition MetricDefinition, profile OperatingProfile, dt time.Duration) float64 {
	if dt <= 0 {
		return 0
	}
	profile = NormalizeOperatingProfile(profile)
	hours := dt.Hours()
	unit := strings.ToLower(strings.TrimSpace(definition.Unit))
	kind := strings.ToUpper(strings.TrimSpace(definition.Kind))
	if kind == "" {
		kind = "GAUGE"
	}
	if kind != "COUNTER" {
		return 0
	}
	if definition.RatePerHour > 0 {
		return hours * definition.RatePerHour
	}
	switch {
	case unit == "km" || strings.Contains(unit, "kilomet"):
		return hours * profile.AvgSpeedKmh
	case unit == "h" || unit == "hr" || unit == "hour" || unit == "hours":
		return hours
	case unit == "kwh":
		return hours * profile.RatedKw
	case unit == "t" || unit == "ton" || unit == "tons" || strings.Contains(unit, "ton"):
		return hours * profile.TonsPerHour
	case unit == "cycle" || unit == "cycles" || strings.Contains(unit, "cycle"):
		return hours * profile.CyclesPerHour
	default:
		return hours
	}
}
