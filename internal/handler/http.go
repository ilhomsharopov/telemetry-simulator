package handler

import (
	"bufio"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"

	"equipment-telemetry-simulator/internal/model"
	"equipment-telemetry-simulator/internal/simulator"
)

type API struct {
	manager *simulator.Manager
	logger  *slog.Logger
}

func NewAPI(manager *simulator.Manager, logger *slog.Logger) *API {
	if logger == nil {
		logger = slog.Default()
	}
	return &API{manager: manager, logger: logger}
}

func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("OPTIONS /", a.options)
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("POST /api/v1/asset-types", a.createAssetType)
	mux.HandleFunc("PUT /api/v1/asset-types", a.upsertAssetType)
	mux.HandleFunc("GET /api/v1/asset-types", a.listAssetTypes)
	mux.HandleFunc("POST /api/v1/assets", a.registerAsset)
	mux.HandleFunc("PUT /api/v1/assets", a.ensureAsset)
	mux.HandleFunc("GET /api/v1/assets", a.listAssets)
	mux.HandleFunc("PUT /api/v1/assets/{assetId}/faults", a.replaceFaults)
	mux.HandleFunc("PUT /api/v1/assets/{assetId}/operating-profile", a.setOperatingProfile)
	mux.HandleFunc("PUT /api/v1/assets/{assetId}/running", a.setRunning)
	mux.HandleFunc("GET /api/v1/ws", a.websocket)

	return recoverMiddleware(corsMiddleware(loggingMiddleware(a.logger, mux)))
}

func (a *API) options(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) createAssetType(w http.ResponseWriter, r *http.Request) {
	var req model.CreateAssetTypeRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.ID == nil || req.Name == nil || req.Metrics == nil {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "id, name, and metrics are required"})
		return
	}

	definition := model.AssetTypeDefinition{
		ID:      *req.ID,
		Name:    *req.Name,
		Metrics: model.MetricDefinitions(*req.Metrics),
	}
	if req.Description != nil {
		definition.Description = *req.Description
	}
	if req.FaultTypes != nil {
		definition.FaultTypes = *req.FaultTypes
	}

	created, err := a.manager.CreateAssetType(definition)
	if err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (a *API) upsertAssetType(w http.ResponseWriter, r *http.Request) {
	var req model.CreateAssetTypeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ID == nil || req.Name == nil || req.Metrics == nil {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "id, name, and metrics are required"})
		return
	}
	definition := model.AssetTypeDefinition{
		ID:      *req.ID,
		Name:    *req.Name,
		Metrics: model.MetricDefinitions(*req.Metrics),
	}
	if req.Description != nil {
		definition.Description = *req.Description
	}
	if req.FaultTypes != nil {
		definition.FaultTypes = *req.FaultTypes
	}
	saved, err := a.manager.UpsertAssetType(definition)
	if err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (a *API) ensureAsset(w http.ResponseWriter, r *http.Request) {
	var req model.RegisterAssetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.AssetID == nil || req.AssetTypeID == nil {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "assetId and assetTypeId are required"})
		return
	}
	asset, err := a.manager.EnsureAsset(*req.AssetID, *req.AssetTypeID)
	if err != nil {
		writeManagerError(w, err)
		return
	}
	if req.OperatingProfile != nil {
		asset, err = a.manager.SetOperatingProfile(*req.AssetID, *req.OperatingProfile)
		if err != nil {
			writeManagerError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, asset)
}

func (a *API) listAssetTypes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.manager.ListAssetTypes())
}

func (a *API) registerAsset(w http.ResponseWriter, r *http.Request) {
	var req model.RegisterAssetRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.AssetID == nil || req.AssetTypeID == nil {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "assetId and assetTypeId are required"})
		return
	}

	asset, err := a.manager.RegisterAsset(*req.AssetID, *req.AssetTypeID)
	if err != nil {
		writeManagerError(w, err)
		return
	}
	if req.OperatingProfile != nil {
		asset, err = a.manager.SetOperatingProfile(*req.AssetID, *req.OperatingProfile)
		if err != nil {
			writeManagerError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, asset)
}

func (a *API) listAssets(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.manager.ListAssets())
}

func (a *API) replaceFaults(w http.ResponseWriter, r *http.Request) {
	var req model.ReplaceFaultsRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.FaultTypes == nil {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "faultTypes is required"})
		return
	}

	asset, err := a.manager.ReplaceFaults(r.PathValue("assetId"), *req.FaultTypes)
	if err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, asset)
}

func (a *API) setOperatingProfile(w http.ResponseWriter, r *http.Request) {
	var profile model.OperatingProfile
	if !decodeJSON(w, r, &profile) {
		return
	}
	asset, err := a.manager.SetOperatingProfile(r.PathValue("assetId"), profile)
	if err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, asset)
}

func (a *API) setRunning(w http.ResponseWriter, r *http.Request) {
	var req model.SetRunningRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Running == nil {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "running is required"})
		return
	}
	asset, err := a.manager.SetRunning(r.PathValue("assetId"), *req.Running)
	if err != nil {
		writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, asset)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: "invalid JSON body: " + err.Error()})
		return false
	}
	return true
}

func writeManagerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, simulator.ErrMissingIdentifier), errors.Is(err, simulator.ErrInvalidRequest):
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: err.Error()})
	case errors.Is(err, simulator.ErrAssetNotFound), errors.Is(err, simulator.ErrAssetTypeNotFound):
		writeJSON(w, http.StatusNotFound, model.ErrorResponse{Error: err.Error()})
	case errors.Is(err, simulator.ErrAssetExists), errors.Is(err, simulator.ErrAssetTypeExists):
		writeJSON(w, http.StatusConflict, model.ErrorResponse{Error: err.Error()})
	case errors.Is(err, simulator.ErrUnsupportedFault):
		writeJSON(w, http.StatusBadRequest, model.ErrorResponse{Error: err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "internal server error"})
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Access-Control-Request-Private-Network")
		// Chrome Private Network Access: public site (toir.tenzorsoft.uz) -> LAN IP (192.168.x.x)
		w.Header().Set("Access-Control-Allow-Private-Network", "true")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

func loggingMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		logger.Info("request completed", "method", r.Method, "path", r.URL.Path, "status", recorder.status)
	})
}

func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				writeJSON(w, http.StatusInternalServerError, model.ErrorResponse{Error: "internal server error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
