package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
)

type MetricDefinition struct {
	Name             string  `json:"name" gorm:"not null"`
	Unit             string  `json:"unit" gorm:"not null"`
	Kind             string  `json:"kind,omitempty"`
	Min              float64 `json:"min"`
	Max              float64 `json:"max"`
	Drift            float64 `json:"drift"`
	InitialValue     float64 `json:"initialValue,omitempty"`
	IncrementPerTick float64 `json:"incrementPerTick,omitempty"`
}

type MetricDefinitions []MetricDefinition

func (m MetricDefinitions) Value() (driver.Value, error) {
	if m == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(m)
}

func (m *MetricDefinitions) Scan(value any) error {
	if value == nil {
		*m = MetricDefinitions{}
		return nil
	}

	var raw []byte
	switch v := value.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("scan metric definitions: unsupported type %T", value)
	}

	if len(raw) == 0 {
		*m = MetricDefinitions{}
		return nil
	}
	return json.Unmarshal(raw, m)
}

type AssetTypeDefinition struct {
	ID          string            `json:"id" gorm:"primaryKey;type:varchar(80);column:id"`
	Name        string            `json:"name" gorm:"not null;type:varchar(160)"`
	Description string            `json:"description" gorm:"type:text"`
	Metrics     MetricDefinitions `json:"metrics" gorm:"type:jsonb;not null;default:'[]'"`
	FaultTypes  pq.StringArray    `json:"faultTypes" gorm:"type:text[];not null;default:'{}'"`
	CreatedAt   time.Time         `json:"createdAt" gorm:"autoCreateTime"`
	UpdatedAt   time.Time         `json:"updatedAt" gorm:"autoUpdateTime"`
}

type AssetStatus string

const (
	AssetStatusRunning AssetStatus = "RUNNING"
	AssetStatusFault   AssetStatus = "FAULT"
	AssetStatusStopped AssetStatus = "STOPPED"
)

type MetricValue struct {
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

type MetricsMap map[string]MetricValue

func (m MetricsMap) Value() (driver.Value, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

func (m *MetricsMap) Scan(value any) error {
	if value == nil {
		*m = MetricsMap{}
		return nil
	}

	var raw []byte
	switch v := value.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("scan metrics map: unsupported type %T", value)
	}

	if len(raw) == 0 {
		*m = MetricsMap{}
		return nil
	}
	return json.Unmarshal(raw, m)
}

type Asset struct {
	AssetID      string         `json:"assetId" gorm:"primaryKey;type:varchar(120);column:asset_id"`
	AssetTypeID  string         `json:"assetTypeId" gorm:"not null;type:varchar(80);index;column:asset_type_id"`
	Status       AssetStatus    `json:"status" gorm:"type:varchar(20);not null;default:'RUNNING'"`
	Metrics      MetricsMap     `json:"metrics" gorm:"type:jsonb;not null;default:'{}'"`
	ActiveFaults pq.StringArray `json:"activeFaults" gorm:"type:text[];not null;default:'{}';column:active_faults"`
	UpdatedAt    time.Time      `json:"updatedAt" gorm:"autoUpdateTime"`
}

type CreateAssetTypeRequest struct {
	ID          *string             `json:"id"`
	Name        *string             `json:"name"`
	Description *string             `json:"description,omitempty"`
	Metrics     *[]MetricDefinition `json:"metrics"`
	FaultTypes  *[]string           `json:"faultTypes,omitempty"`
}

type RegisterAssetRequest struct {
	AssetID     *string `json:"assetId"`
	AssetTypeID *string `json:"assetTypeId"`
}

type ReplaceFaultsRequest struct {
	FaultTypes *[]string `json:"faultTypes"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

func (s AssetStatus) Valid() bool {
	switch s {
	case AssetStatusRunning, AssetStatusFault, AssetStatusStopped:
		return true
	default:
		return false
	}
}

func ValidateMetricDefinitions(metrics []MetricDefinition) error {
	if len(metrics) == 0 {
		return errors.New("metrics must contain at least one metric definition")
	}

	seen := make(map[string]struct{}, len(metrics))
	for _, metric := range metrics {
		kind := metric.Kind
		if kind == "" {
			kind = "GAUGE"
		}
		if metric.Name == "" {
			return errors.New("metric name is required")
		}
		if metric.Unit == "" {
			return fmt.Errorf("metric %q unit is required", metric.Name)
		}
		if kind != "COUNTER" && metric.Min >= metric.Max {
			return fmt.Errorf("metric %q min must be less than max", metric.Name)
		}
		if kind != "GAUGE" && kind != "COUNTER" && kind != "LEVEL" {
			return fmt.Errorf("metric %q has unsupported kind %q", metric.Name, metric.Kind)
		}
		if _, exists := seen[metric.Name]; exists {
			return fmt.Errorf("duplicate metric definition %q", metric.Name)
		}
		seen[metric.Name] = struct{}{}
	}
	return nil
}
