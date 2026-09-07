package simulator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"equipment-telemetry-simulator/internal/model"

	"gorm.io/gorm"
)

var (
	ErrAssetExists       = errors.New("asset already exists")
	ErrAssetNotFound     = errors.New("asset not found")
	ErrAssetTypeExists   = errors.New("asset type already exists")
	ErrAssetTypeNotFound = errors.New("asset type not found")
	ErrUnsupportedFault  = errors.New("unsupported fault type")
	ErrMissingIdentifier = errors.New("assetId is required")
	ErrInvalidRequest    = errors.New("invalid request")
)

type Manager struct {
	mu          sync.RWMutex
	db          *gorm.DB
	assetTypes  map[string]model.AssetTypeDefinition
	assets      map[string]*model.Asset
	tick        time.Duration
	rng         *rand.Rand
	logger      *slog.Logger
	pushClient  *PushClient
	pushEvery   time.Duration
	roundDigits int
	lastTick    time.Time
}

type Config struct {
	TickInterval time.Duration
	PushClient   *PushClient
	PushInterval time.Duration
	Logger       *slog.Logger
}

func NewManager(db *gorm.DB, cfg Config) (*Manager, error) {
	if db == nil {
		return nil, errors.New("gorm DB is required")
	}

	tick := cfg.TickInterval
	if tick <= 0 {
		tick = 2 * time.Second
	}

	pushEvery := cfg.PushInterval
	if pushEvery <= 0 {
		pushEvery = tick
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	manager := &Manager{
		db:          db,
		assetTypes:  make(map[string]model.AssetTypeDefinition),
		assets:      make(map[string]*model.Asset),
		tick:        tick,
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())),
		logger:      logger,
		pushClient:  cfg.PushClient,
		pushEvery:   pushEvery,
		roundDigits: 2,
	}

	if err := manager.loadFromDB(); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *Manager) loadFromDB() error {
	var assetTypes []model.AssetTypeDefinition
	if err := m.db.Find(&assetTypes).Error; err != nil {
		return fmt.Errorf("load asset type definitions: %w", err)
	}

	var assets []model.Asset
	if err := m.db.Find(&assets).Error; err != nil {
		return fmt.Errorf("load assets: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, assetType := range assetTypes {
		m.assetTypes[assetType.ID] = assetType
	}
	for i := range assets {
		asset := assets[i]
		asset.ActiveFaults = dedupeStrings(asset.ActiveFaults)
		if asset.Metrics == nil {
			asset.Metrics = model.MetricsMap{}
		}
		m.assets[asset.AssetID] = &asset
	}
	return nil
}

func (m *Manager) Start(ctx context.Context) {
	go m.runTicker(ctx)
	if m.pushClient != nil {
		go m.runPusher(ctx)
	}
}

func (m *Manager) CreateAssetType(def model.AssetTypeDefinition) (model.AssetTypeDefinition, error) {
	def.ID = strings.TrimSpace(def.ID)
	def.Name = strings.TrimSpace(def.Name)
	if def.ID == "" {
		return model.AssetTypeDefinition{}, fmt.Errorf("%w: asset type id is required", ErrInvalidRequest)
	}
	if def.Name == "" {
		return model.AssetTypeDefinition{}, fmt.Errorf("%w: asset type name is required", ErrInvalidRequest)
	}
	if err := model.ValidateMetricDefinitions(def.Metrics); err != nil {
		return model.AssetTypeDefinition{}, fmt.Errorf("%w: %s", ErrInvalidRequest, err)
	}
	def.FaultTypes = dedupeStrings(def.FaultTypes)

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.assetTypes[def.ID]; exists {
		return model.AssetTypeDefinition{}, ErrAssetTypeExists
	}

	if err := m.db.Create(&def).Error; err != nil {
		return model.AssetTypeDefinition{}, fmt.Errorf("create asset type definition: %w", err)
	}
	m.assetTypes[def.ID] = def
	return def, nil
}

func (m *Manager) UpsertAssetType(def model.AssetTypeDefinition) (model.AssetTypeDefinition, error) {
	def.ID = strings.TrimSpace(def.ID)
	def.Name = strings.TrimSpace(def.Name)
	if def.ID == "" {
		return model.AssetTypeDefinition{}, fmt.Errorf("%w: asset type id is required", ErrInvalidRequest)
	}
	if def.Name == "" {
		return model.AssetTypeDefinition{}, fmt.Errorf("%w: asset type name is required", ErrInvalidRequest)
	}
	if err := model.ValidateMetricDefinitions(def.Metrics); err != nil {
		return model.AssetTypeDefinition{}, fmt.Errorf("%w: %s", ErrInvalidRequest, err)
	}
	def.FaultTypes = dedupeStrings(def.FaultTypes)

	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, exists := m.assetTypes[def.ID]; exists {
		def.CreatedAt = existing.CreatedAt
		if err := m.db.Save(&def).Error; err != nil {
			return model.AssetTypeDefinition{}, fmt.Errorf("update asset type definition: %w", err)
		}
	} else {
		if err := m.db.Create(&def).Error; err != nil {
			return model.AssetTypeDefinition{}, fmt.Errorf("create asset type definition: %w", err)
		}
	}
	m.assetTypes[def.ID] = def
	return def, nil
}

func (m *Manager) EnsureAsset(assetID, assetTypeID string) (model.Asset, error) {
	assetID = strings.TrimSpace(assetID)
	assetTypeID = strings.TrimSpace(assetTypeID)
	if assetID == "" {
		return model.Asset{}, ErrMissingIdentifier
	}
	if assetTypeID == "" {
		return model.Asset{}, fmt.Errorf("%w: assetTypeId is required", ErrInvalidRequest)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	assetType, ok := m.assetTypes[assetTypeID]
	if !ok {
		return model.Asset{}, fmt.Errorf("%w: %s", ErrAssetTypeNotFound, assetTypeID)
	}

	if existing, exists := m.assets[assetID]; exists {
		existing.AssetTypeID = assetTypeID
		m.syncAssetMetricsLocked(existing, assetType)
		m.applyTypeInitialValuesLocked(existing, assetType)
		existing.UpdatedAt = time.Now().UTC()
		if err := m.db.Save(existing).Error; err != nil {
			return model.Asset{}, fmt.Errorf("update asset: %w", err)
		}
		return cloneAsset(existing), nil
	}

	asset := &model.Asset{
		AssetID:          assetID,
		AssetTypeID:      assetTypeID,
		Status:           model.AssetStatusRunning,
		Metrics:          m.initialMetricsLocked(assetType),
		ActiveFaults:     []string{},
		OperatingProfile: model.DefaultContinuousProfile(),
		UpdatedAt:        time.Now().UTC(),
	}
	if err := m.db.Create(asset).Error; err != nil {
		return model.Asset{}, fmt.Errorf("create asset: %w", err)
	}
	m.assets[assetID] = asset
	return cloneAsset(asset), nil
}

func (m *Manager) applyTypeInitialValuesLocked(asset *model.Asset, assetType model.AssetTypeDefinition) {
	for _, definition := range assetType.Metrics {
		m.setMetric(asset, definition, m.initialMetricValueLocked(definition))
	}
}

func (m *Manager) ListAssetTypes() []model.AssetTypeDefinition {
	m.mu.RLock()
	defer m.mu.RUnlock()

	definitions := make([]model.AssetTypeDefinition, 0, len(m.assetTypes))
	for _, definition := range m.assetTypes {
		definitions = append(definitions, definition)
	}
	return definitions
}

func (m *Manager) RegisterAsset(assetID, assetTypeID string) (model.Asset, error) {
	assetID = strings.TrimSpace(assetID)
	assetTypeID = strings.TrimSpace(assetTypeID)
	if assetID == "" {
		return model.Asset{}, ErrMissingIdentifier
	}
	if assetTypeID == "" {
		return model.Asset{}, fmt.Errorf("%w: assetTypeId is required", ErrInvalidRequest)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	assetType, ok := m.assetTypes[assetTypeID]
	if !ok {
		return model.Asset{}, fmt.Errorf("%w: %s", ErrAssetTypeNotFound, assetTypeID)
	}
	if _, exists := m.assets[assetID]; exists {
		return model.Asset{}, ErrAssetExists
	}

	asset := &model.Asset{
		AssetID:          assetID,
		AssetTypeID:      assetTypeID,
		Status:           model.AssetStatusRunning,
		Metrics:          m.initialMetricsLocked(assetType),
		ActiveFaults:     []string{},
		OperatingProfile: model.DefaultContinuousProfile(),
		UpdatedAt:        time.Now().UTC(),
	}

	if err := m.db.Create(asset).Error; err != nil {
		return model.Asset{}, fmt.Errorf("create asset: %w", err)
	}
	m.assets[assetID] = asset
	return cloneAsset(asset), nil
}

func (m *Manager) SetOperatingProfile(assetID string, profile model.OperatingProfile) (model.Asset, error) {
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return model.Asset{}, ErrMissingIdentifier
	}
	profile = model.NormalizeOperatingProfile(profile)
	switch profile.Mode {
	case model.OperatingModeContinuous, model.OperatingModeShift, model.OperatingModeOutOfService:
	default:
		return model.Asset{}, fmt.Errorf("%w: unsupported operating mode", ErrInvalidRequest)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	asset, ok := m.lookupAssetLocked(assetID)
	if !ok {
		return model.Asset{}, ErrAssetNotFound
	}
	asset.OperatingProfile = profile
	if profile.Mode == model.OperatingModeOutOfService {
		asset.Status = model.AssetStatusStopped
	} else {
		asset.ResumeProfile = nil
		if len(asset.ActiveFaults) > 0 {
			asset.Status = model.AssetStatusFault
		} else {
			asset.Status = model.AssetStatusRunning
		}
	}
	asset.UpdatedAt = time.Now().UTC()
	if err := m.db.Save(asset).Error; err != nil {
		return model.Asset{}, fmt.Errorf("save operating profile: %w", err)
	}
	return cloneAsset(asset), nil
}

func (m *Manager) SetRunning(assetID string, running bool) (model.Asset, error) {
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return model.Asset{}, ErrMissingIdentifier
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	asset, ok := m.lookupAssetLocked(assetID)
	if !ok {
		return model.Asset{}, ErrAssetNotFound
	}

	current := model.NormalizeOperatingProfile(asset.OperatingProfile)
	if running {
		resume := current
		if current.Mode == model.OperatingModeOutOfService {
			if asset.ResumeProfile != nil {
				resume = model.NormalizeOperatingProfile(*asset.ResumeProfile)
			} else {
				resume = model.DefaultContinuousProfile()
			}
		}
		if resume.Mode == model.OperatingModeOutOfService {
			resume = model.DefaultContinuousProfile()
		}
		resume.StoppedUntil = nil
		asset.OperatingProfile = resume
		asset.ResumeProfile = nil
		if len(asset.ActiveFaults) > 0 {
			asset.Status = model.AssetStatusFault
		} else {
			asset.Status = model.AssetStatusRunning
		}
	} else {
		if current.Mode != model.OperatingModeOutOfService {
			copied := current
			asset.ResumeProfile = &copied
		}
		asset.OperatingProfile = model.NormalizeOperatingProfile(model.OperatingProfile{
			Mode: model.OperatingModeOutOfService,
		})
		if len(asset.ActiveFaults) == 0 {
			asset.Status = model.AssetStatusStopped
		}
	}
	asset.UpdatedAt = time.Now().UTC()
	if err := m.db.Save(asset).Error; err != nil {
		return model.Asset{}, fmt.Errorf("save running state: %w", err)
	}
	return cloneAsset(asset), nil
}

func (m *Manager) ListAssets() []model.Asset {
	m.mu.RLock()
	defer m.mu.RUnlock()

	assets := make([]model.Asset, 0, len(m.assets))
	for _, asset := range m.assets {
		assets = append(assets, cloneAsset(asset))
	}
	sort.Slice(assets, func(i, j int) bool {
		return assets[i].UpdatedAt.After(assets[j].UpdatedAt)
	})
	return assets
}

func (m *Manager) ReplaceFaults(assetID string, faultTypes []string) (model.Asset, error) {
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return model.Asset{}, ErrMissingIdentifier
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	asset, ok := m.lookupAssetLocked(assetID)
	if !ok {
		return model.Asset{}, ErrAssetNotFound
	}
	assetType, ok := m.assetTypes[asset.AssetTypeID]
	if !ok {
		return model.Asset{}, fmt.Errorf("%w: %s", ErrAssetTypeNotFound, asset.AssetTypeID)
	}

	faultTypes = dedupeStrings(faultTypes)
	for _, faultType := range faultTypes {
		if !stringInSlice(faultType, assetType.FaultTypes) {
			return model.Asset{}, fmt.Errorf("%w: %s", ErrUnsupportedFault, faultType)
		}
	}

	asset.ActiveFaults = faultTypes
	if len(faultTypes) == 0 {
		asset.Status = model.AssetStatusRunning
		m.restoreNormalMetricsLocked(asset, assetType)
	} else {
		asset.Status = model.AssetStatusFault
		m.restoreNormalMetricsLocked(asset, assetType)
		m.applyFaultsLocked(asset, assetType)
	}
	asset.UpdatedAt = time.Now().UTC()

	if err := m.db.Save(asset).Error; err != nil {
		return model.Asset{}, fmt.Errorf("save asset faults: %w", err)
	}
	return cloneAsset(asset), nil
}

func (m *Manager) runTicker(ctx context.Context) {
	ticker := time.NewTicker(m.tick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.step(); err != nil {
				m.logger.Warn("simulation tick failed", "error", err)
			}
		}
	}
}

func (m *Manager) runPusher(ctx context.Context) {
	ticker := time.NewTicker(m.pushEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			result, err := m.pushClient.Push(ctx, m.ListAssets())
			if err != nil {
				m.logger.Warn("telemetry push failed", "error", err)
				continue
			}
			if result != nil && (result.Accepted > 0 || result.Rejected > 0) {
				m.logger.Info("telemetry push",
					"accepted", result.Accepted,
					"skipped", result.Skipped,
					"rejected", result.Rejected)
			}
		}
	}
}

func (m *Manager) step() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	changed := make([]*model.Asset, 0, len(m.assets))
	now := time.Now().UTC()
	dt := m.tick
	if !m.lastTick.IsZero() {
		dt = now.Sub(m.lastTick)
	}
	m.lastTick = now
	dt = clampSimulationDelta(dt, m.tick)

	for _, asset := range m.assets {
		assetType, ok := m.assetTypes[asset.AssetTypeID]
		if !ok {
			m.logger.Warn("asset type missing for asset", "assetId", asset.AssetID, "assetTypeId", asset.AssetTypeID)
			continue
		}

		operating := model.IsOperating(now, asset.OperatingProfile)
		if !operating {
			if len(asset.ActiveFaults) == 0 {
				asset.Status = model.AssetStatusStopped
			}
			asset.UpdatedAt = now
			changed = append(changed, asset)
			continue
		}

		m.tickAssetLocked(asset, assetType, dt)
		if len(asset.ActiveFaults) > 0 {
			asset.Status = model.AssetStatusFault
			m.applyFaultsLocked(asset, assetType)
		} else {
			asset.Status = model.AssetStatusRunning
		}
		asset.UpdatedAt = now
		changed = append(changed, asset)
	}

	if len(changed) == 0 {
		return nil
	}

	return m.db.Transaction(func(tx *gorm.DB) error {
		for _, asset := range changed {
			if err := tx.Model(&model.Asset{}).
				Where("asset_id = ?", asset.AssetID).
				Updates(map[string]any{
					"status":            asset.Status,
					"metrics":           asset.Metrics,
					"active_faults":     asset.ActiveFaults,
					"operating_profile": asset.OperatingProfile,
					"resume_profile":    asset.ResumeProfile,
					"updated_at":        asset.UpdatedAt,
				}).Error; err != nil {
				return fmt.Errorf("update asset %s: %w", asset.AssetID, err)
			}
		}
		return nil
	})
}

func (m *Manager) tickAssetLocked(asset *model.Asset, assetType model.AssetTypeDefinition, dt time.Duration) {
	if asset.Metrics == nil {
		asset.Metrics = model.MetricsMap{}
	}

	for _, definition := range assetType.Metrics {
		kind := metricKind(definition)
		current, ok := asset.Metrics[definition.Name]
		if !ok {
			current = model.MetricValue{
				Value: m.initialMetricValueLocked(definition),
				Unit:  definition.Unit,
			}
		}

		drift := definition.Drift
		if drift <= 0 {
			drift = (definition.Max - definition.Min) * 0.05
		}

		next := current.Value
		switch kind {
		case "COUNTER":
			next = current.Value + model.CounterDelta(definition, asset.OperatingProfile, dt)
		case "LEVEL":
			rate := definition.RatePerHour
			if rate <= 0 {
				rate = math.Abs(drift)
			}
			next = current.Value - rate*dt.Hours()
			if next < definition.Min {
				next = definition.Min
			}
		default:
			next = current.Value + m.randomBetween(-drift, drift)
			if next < definition.Min || next > definition.Max {
				next = m.randomBetween(definition.Min, definition.Max)
			}
		}

		places := 2
		if kind == "COUNTER" {
			places = 6
		}
		asset.Metrics[definition.Name] = model.MetricValue{
			Value: m.roundPlaces(next, places),
			Unit:  definition.Unit,
		}
	}
}

func (m *Manager) applyFaultsLocked(asset *model.Asset, assetType model.AssetTypeDefinition) {
	if len(assetType.Metrics) == 0 {
		return
	}

	for _, faultType := range asset.ActiveFaults {
		upperFault := strings.ToUpper(faultType)
		applied := false

		for _, metric := range assetType.Metrics {
			metricName := strings.ToUpper(metric.Name)
			drift := metric.Drift
			if drift <= 0 {
				drift = math.Max((metric.Max-metric.Min)*0.2, 1)
			}

			switch {
			case strings.Contains(upperFault, "OVERHEAT") && strings.Contains(metricName, "TEMP"):
				m.setMetric(asset, metric, metric.Max+drift*4)
				applied = true
			case strings.Contains(upperFault, "LOW_PRESSURE") && strings.Contains(metricName, "PRESSURE"):
				m.setMetric(asset, metric, metric.Min-drift*4)
				applied = true
			case strings.Contains(upperFault, "PRESSURE_DROP") && strings.Contains(metricName, "PRESSURE"):
				m.setMetric(asset, metric, metric.Min-drift*4)
				applied = true
			case strings.Contains(upperFault, "HIGH_PRESSURE") && strings.Contains(metricName, "PRESSURE"):
				m.setMetric(asset, metric, metric.Max+drift*4)
				applied = true
			case strings.Contains(upperFault, "SURGE") && (strings.Contains(metricName, "VOLT") || strings.Contains(metricName, "RPM")):
				m.setMetric(asset, metric, metric.Max+drift*5)
				applied = true
			case strings.Contains(upperFault, "LEAK") && (strings.Contains(metricName, "FUEL") || strings.Contains(metricName, "FLOW") || strings.Contains(metricName, "VOLUME")):
				m.setMetric(asset, metric, metric.Min-drift*3)
				applied = true
			case strings.Contains(upperFault, "VIBRATION") && strings.Contains(metricName, "VIBRATION"):
				m.setMetric(asset, metric, metric.Max+drift*6)
				applied = true
			}
		}

		if !applied {
			metric := assetType.Metrics[0]
			drift := metric.Drift
			if drift <= 0 {
				drift = math.Max((metric.Max-metric.Min)*0.2, 1)
			}
			m.setMetric(asset, metric, metric.Max+drift*3)
		}
	}
}

func (m *Manager) restoreNormalMetricsLocked(asset *model.Asset, assetType model.AssetTypeDefinition) {
	if asset.Metrics == nil {
		asset.Metrics = model.MetricsMap{}
	}

	for _, definition := range assetType.Metrics {
		current, ok := asset.Metrics[definition.Name]
		if !ok || (metricKind(definition) != "COUNTER" && (current.Value < definition.Min || current.Value > definition.Max)) {
			current = model.MetricValue{
				Value: m.initialMetricValueLocked(definition),
				Unit:  definition.Unit,
			}
		}
		current.Unit = definition.Unit
		current.Value = m.round(current.Value)
		asset.Metrics[definition.Name] = current
	}
}

func (m *Manager) syncAssetMetricsLocked(asset *model.Asset, assetType model.AssetTypeDefinition) {
	if asset.Metrics == nil {
		asset.Metrics = model.MetricsMap{}
	}
	keep := make(map[string]struct{}, len(assetType.Metrics))
	for _, definition := range assetType.Metrics {
		keep[definition.Name] = struct{}{}
		current, ok := asset.Metrics[definition.Name]
		if !ok {
			current = model.MetricValue{
				Value: m.round(m.initialMetricValueLocked(definition)),
				Unit:  definition.Unit,
			}
		} else {
			current.Unit = definition.Unit
		}
		asset.Metrics[definition.Name] = current
	}
	for name := range asset.Metrics {
		if _, ok := keep[name]; !ok {
			delete(asset.Metrics, name)
		}
	}
}

func (m *Manager) initialMetricsLocked(assetType model.AssetTypeDefinition) model.MetricsMap {
	metrics := make(model.MetricsMap, len(assetType.Metrics))
	for _, definition := range assetType.Metrics {
		metrics[definition.Name] = model.MetricValue{
			Value: m.round(m.initialMetricValueLocked(definition)),
			Unit:  definition.Unit,
		}
	}
	return metrics
}

func (m *Manager) initialMetricValueLocked(definition model.MetricDefinition) float64 {
	if definition.InitialValue != 0 {
		return definition.InitialValue
	}
	if metricKind(definition) == "COUNTER" {
		return math.Max(definition.Min, 0)
	}
	return m.randomBetween(definition.Min, definition.Max)
}

func (m *Manager) setMetric(asset *model.Asset, definition model.MetricDefinition, value float64) {
	asset.Metrics[definition.Name] = model.MetricValue{
		Value: m.round(value),
		Unit:  definition.Unit,
	}
}

func (m *Manager) randomBetween(minValue, maxValue float64) float64 {
	if maxValue <= minValue {
		return minValue
	}
	return minValue + m.rng.Float64()*(maxValue-minValue)
}

func (m *Manager) round(value float64) float64 {
	return m.roundPlaces(value, m.roundDigits)
}

func (m *Manager) roundPlaces(value float64, places int) float64 {
	if places < 0 {
		places = 0
	}
	scale := math.Pow(10, float64(places))
	return math.Round(value*scale) / scale
}

func cloneAsset(asset *model.Asset) model.Asset {
	clone := model.Asset{
		AssetID:          asset.AssetID,
		AssetTypeID:      asset.AssetTypeID,
		Status:           asset.Status,
		Metrics:          make(model.MetricsMap, len(asset.Metrics)),
		ActiveFaults:     append([]string(nil), asset.ActiveFaults...),
		OperatingProfile: asset.OperatingProfile,
		UpdatedAt:        asset.UpdatedAt,
	}
	if clone.ActiveFaults == nil {
		clone.ActiveFaults = []string{}
	}
	if asset.ResumeProfile != nil {
		copied := *asset.ResumeProfile
		clone.ResumeProfile = &copied
	}
	for name, metric := range asset.Metrics {
		clone.Metrics[name] = metric
	}
	return clone
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	deduped := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		deduped = append(deduped, value)
	}
	return deduped
}

const maxSimulationDelta = 30 * time.Second

func clampSimulationDelta(dt, tick time.Duration) time.Duration {
	if dt <= 0 {
		return tick
	}
	if dt > maxSimulationDelta {
		return maxSimulationDelta
	}
	return dt
}

func (m *Manager) lookupAssetLocked(assetID string) (*model.Asset, bool) {
	if asset, ok := m.assets[assetID]; ok {
		return asset, true
	}
	for _, candidate := range assetIDAliases(assetID) {
		if asset, ok := m.assets[candidate]; ok {
			return asset, true
		}
	}
	return nil, false
}

func assetIDAliases(assetID string) []string {
	trimmed := strings.TrimSpace(assetID)
	aliases := make([]string, 0, 4)
	seen := map[string]struct{}{trimmed: {}}
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		aliases = append(aliases, id)
	}
	if canonical, ok := canonicalToirEquipmentID(trimmed); ok {
		add(canonical)
		add("eq-" + canonical)
		add("toir-" + canonical)
	}
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(lower, "eq-"):
		add(trimmed[3:])
	case strings.HasPrefix(lower, "toir-"):
		add(trimmed[5:])
	default:
		add("eq-" + trimmed)
		add("toir-" + trimmed)
	}
	return aliases
}

func metricKind(definition model.MetricDefinition) string {
	if definition.Kind == "" {
		return "GAUGE"
	}
	return strings.ToUpper(definition.Kind)
}

func stringInSlice(value string, values []string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
