package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"equipment-telemetry-simulator/internal/model"

	"github.com/gorilla/websocket"
)

const (
	wsWriteWait      = 10 * time.Second
	wsPongWait       = 60 * time.Second
	wsPingPeriod     = (wsPongWait * 9) / 10
	wsMaxMessageSize = 1 << 20
	wsStreamInterval = 2 * time.Second
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     allowWebSocketOrigin,
}

func allowWebSocketOrigin(r *http.Request) bool {
	raw := strings.TrimSpace(os.Getenv("ALLOWED_ORIGINS"))
	if raw == "" {
		return true
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	for _, allowed := range strings.Split(raw, ",") {
		allowed = strings.TrimSpace(allowed)
		if allowed == "*" || strings.EqualFold(allowed, origin) {
			return true
		}
	}
	return false
}

type wsRequest struct {
	RequestID string          `json:"requestId,omitempty"`
	Action    string          `json:"action"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

type wsResponse struct {
	RequestID string `json:"requestId,omitempty"`
	Type      string `json:"type"`
	Action    string `json:"action,omitempty"`
	OK        bool   `json:"ok"`
	Data      any    `json:"data,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (a *API) websocket(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		a.logger.Warn("websocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	conn.SetReadLimit(wsMaxMessageSize)
	_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsPongWait))
	})

	requests := make(chan wsRequest)
	done := make(chan struct{})

	go readWSRequests(conn, requests, done)

	subscribedAssets := false
	streamTicker := time.NewTicker(wsStreamInterval)
	defer streamTicker.Stop()

	pingTicker := time.NewTicker(wsPingPeriod)
	defer pingTicker.Stop()

	if !writeWS(conn, wsResponse{
		Type: "connected",
		OK:   true,
		Data: map[string]any{
			"actions": []string{
				"asset_types.list",
				"asset_types.create",
				"assets.list",
				"assets.register",
				"assets.faults.replace",
				"assets.running.set",
				"assets.subscribe",
				"assets.unsubscribe",
				"ping",
			},
		},
	}) {
		return
	}

	for {
		select {
		case <-done:
			return
		case <-pingTicker.C:
			if err := conn.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(wsWriteWait)); err != nil {
				return
			}
		case <-streamTicker.C:
			if subscribedAssets {
				if !writeWS(conn, wsResponse{
					Type: "event",
					OK:   true,
					Data: map[string]any{
						"event":  "assets.snapshot",
						"assets": a.manager.ListAssets(),
						"sentAt": time.Now().UTC(),
					},
				}) {
					return
				}
			}
		case req := <-requests:
			response, subscribe, unsubscribe := a.handleWSRequest(req)
			if subscribe {
				subscribedAssets = true
			}
			if unsubscribe {
				subscribedAssets = false
			}
			if !writeWS(conn, response) {
				return
			}
			if subscribe {
				if !writeWS(conn, wsResponse{
					Type: "event",
					OK:   true,
					Data: map[string]any{
						"event":  "assets.snapshot",
						"assets": a.manager.ListAssets(),
						"sentAt": time.Now().UTC(),
					},
				}) {
					return
				}
			}
		}
	}
}

func readWSRequests(conn *websocket.Conn, requests chan<- wsRequest, done chan<- struct{}) {
	defer close(done)
	for {
		var req wsRequest
		if err := conn.ReadJSON(&req); err != nil {
			return
		}
		requests <- req
	}
}

func (a *API) handleWSRequest(req wsRequest) (wsResponse, bool, bool) {
	response := wsResponse{
		RequestID: req.RequestID,
		Type:      "response",
		Action:    req.Action,
		OK:        true,
	}

	switch req.Action {
	case "ping":
		response.Data = map[string]any{"message": "pong", "sentAt": time.Now().UTC()}
	case "asset_types.list":
		response.Data = a.manager.ListAssetTypes()
	case "asset_types.create":
		var payload model.CreateAssetTypeRequest
		if err := decodeWSPayload(req.Payload, &payload); err != nil {
			return wsError(req, err), false, false
		}
		if payload.ID == nil || payload.Name == nil || payload.Metrics == nil {
			return wsError(req, errors.New("id, name, and metrics are required")), false, false
		}
		definition := model.AssetTypeDefinition{
			ID:      *payload.ID,
			Name:    *payload.Name,
			Metrics: model.MetricDefinitions(*payload.Metrics),
		}
		if payload.Description != nil {
			definition.Description = *payload.Description
		}
		if payload.FaultTypes != nil {
			definition.FaultTypes = *payload.FaultTypes
		}
		created, err := a.manager.CreateAssetType(definition)
		if err != nil {
			return wsError(req, err), false, false
		}
		response.Data = created
	case "assets.list":
		response.Data = a.manager.ListAssets()
	case "assets.register":
		var payload model.RegisterAssetRequest
		if err := decodeWSPayload(req.Payload, &payload); err != nil {
			return wsError(req, err), false, false
		}
		if payload.AssetID == nil || payload.AssetTypeID == nil {
			return wsError(req, errors.New("assetId and assetTypeId are required")), false, false
		}
		asset, err := a.manager.RegisterAsset(*payload.AssetID, *payload.AssetTypeID)
		if err != nil {
			return wsError(req, err), false, false
		}
		if payload.OperatingProfile != nil {
			asset, err = a.manager.SetOperatingProfile(*payload.AssetID, *payload.OperatingProfile)
			if err != nil {
				return wsError(req, err), false, false
			}
		}
		response.Data = asset
	case "assets.faults.replace":
		var payload struct {
			AssetID    *string  `json:"assetId"`
			FaultTypes []string `json:"faultTypes"`
		}
		if err := decodeWSPayload(req.Payload, &payload); err != nil {
			return wsError(req, err), false, false
		}
		if payload.AssetID == nil {
			return wsError(req, errors.New("assetId is required")), false, false
		}
		asset, err := a.manager.ReplaceFaults(*payload.AssetID, payload.FaultTypes)
		if err != nil {
			return wsError(req, err), false, false
		}
		response.Data = asset
	case "assets.running.set":
		var payload struct {
			AssetID *string `json:"assetId"`
			Running *bool   `json:"running"`
		}
		if err := decodeWSPayload(req.Payload, &payload); err != nil {
			return wsError(req, err), false, false
		}
		if payload.AssetID == nil || payload.Running == nil {
			return wsError(req, errors.New("assetId and running are required")), false, false
		}
		asset, err := a.manager.SetRunning(*payload.AssetID, *payload.Running)
		if err != nil {
			return wsError(req, err), false, false
		}
		response.Data = asset
	case "assets.subscribe":
		response.Data = map[string]string{"subscription": "assets.snapshot"}
		return response, true, false
	case "assets.unsubscribe":
		response.Data = map[string]string{"subscription": "none"}
		return response, false, true
	default:
		return wsError(req, errors.New("unsupported websocket action")), false, false
	}

	return response, false, false
}

func decodeWSPayload(payload json.RawMessage, dst any) error {
	if len(payload) == 0 {
		return errors.New("payload is required")
	}
	decoderErr := json.Unmarshal(payload, dst)
	if decoderErr != nil {
		return errors.New("invalid payload: " + decoderErr.Error())
	}
	return nil
}

func wsError(req wsRequest, err error) wsResponse {
	return wsResponse{
		RequestID: req.RequestID,
		Type:      "response",
		Action:    req.Action,
		OK:        false,
		Error:     err.Error(),
	}
}

func writeWS(conn *websocket.Conn, response wsResponse) bool {
	_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
	return conn.WriteJSON(response) == nil
}
