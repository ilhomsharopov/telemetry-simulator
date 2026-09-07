# Equipment Telemetry & Digital Twin Simulator

Go backend for simulating industrial equipment telemetry for TOiR, MRO, and EAM systems.

The simulator uses dynamic asset types stored in PostgreSQL. Asset types define which sensors/counters/metrics an asset has, normal operating ranges, drift behavior, and allowed fault types. The running asset state is kept in memory for speed and synced to PostgreSQL with GORM.

Current implementation status:

- Implemented: dynamic asset types.
- Implemented: one asset can have many telemetry metrics/sensors.
- Implemented: REST API and WebSocket API.
- Implemented: realtime asset snapshot streaming over WebSocket.
- Implemented: explicit metric kind support: `GAUGE`, `COUNTER`, and `LEVEL`.
- Implemented: `GAUGE` simulation for pressure, temperature, volume, flow, and similar values.
- Implemented: `COUNTER` simulation for values like `engine_hours` / `motochas` that only increase.
- Implemented: `LEVEL` simulation for fuel/oil/tank-like values that move downward by configured drift.

## Core Concepts

### Asset

An `Asset` is the real-world equipment or machine being simulated.

Examples:

- Compressor
- Water pump
- Diesel generator
- Heavy truck
- Conveyor motor

An asset is not the same thing as a single sensor/datchik. The asset is the parent equipment.

Example:

```text
Asset: COMP-301
Asset Type: COMPRESSOR
Status: RUNNING / FAULT / STOPPED
```

### Sensor / Metric / Counter

Sensors and counters are represented as dynamic metric definitions inside an asset type.

Example compressor sensors:

- `temperature_c` - temperature sensor
- `pressure_psi` - air pressure sensor
- `volume_m3_min` - air volume/flow sensor
- `oil_volume_liters` - engine oil volume sensor
- `engine_hours` - motochas / engine hours counter

In the API these are called `metrics`, but conceptually each metric can represent one sensor/datchik or one counter/schyotchik.

Example:

```json
{
  "temperature_c": { "value": 74.2, "unit": "C" },
  "pressure_psi": { "value": 101.4, "unit": "psi" },
  "oil_volume_liters": { "value": 18.6, "unit": "L" },
  "engine_hours": { "value": 1240.75, "unit": "h" }
}
```

Important distinction:

```text
Asset = equipment/machine
Sensor = one measuring device inside the asset
Counter = one increasing meter inside the asset
Metric = API representation of sensor or counter value
```

So an excavator should be modeled like this:

```text
Asset: EXC-1001
Asset Type: EXCAVATOR
Metrics/Sensors/Counters:
  - engine_hours       -> counter, only increases
  - hydraulic_temp_c   -> gauge, fluctuates up/down
  - fuel_level_liters  -> level, usually decreases while running
  - oil_pressure_bar   -> gauge, fluctuates up/down
```

### Asset Type Definition

An `AssetTypeDefinition` is a template. It defines what kind of equipment this is and what sensors it has.

Example:

```text
Asset Type: COMPRESSOR
Metrics/Sensors:
  - temperature_c
  - pressure_psi
  - volume_m3_min
  - oil_volume_liters
  - engine_hours
Allowed Faults:
  - OVERHEATING
  - PRESSURE_DROP
  - OIL_LEAK
```

### Running Asset Instance

A registered asset is created from an asset type.

Example:

```text
COMP-301 -> uses COMPRESSOR template
COMP-302 -> uses COMPRESSOR template
PUMP-101 -> uses WATER_PUMP template
```

Each instance has its own current telemetry values and active faults.

## Metric Behavior Types

The simulator needs to support different metric behaviors.

### `GAUGE`

`GAUGE` means the value can go up and down inside a normal range.

Examples:

- pressure
- temperature
- vibration
- air volume / flow
- oil pressure

Example behavior:

```text
temperature_c: 72.1 -> 72.8 -> 71.9 -> 73.2
```

This is what the current backend simulation engine already does implicitly.

### `COUNTER`

`COUNTER` means the value only increases. It must not go down during normal simulation.

Examples:

- engine hours / motochas
- odometer
- total produced units
- total runtime minutes

Example behavior:

```text
engine_hours: 1240.10 -> 1240.11 -> 1240.12 -> 1240.13
```

This behavior is required for excavators, trucks, generators, and other machines where `motochas` is tracked.

Recommended metric definition shape:

```json
{
  "name": "engine_hours",
  "unit": "h",
  "kind": "COUNTER",
  "initialValue": 1240.5,
  "ratePerHour": 1
}
```

Counters grow in real time, not per tick: the delta is `ratePerHour * elapsedHours`, and it is only applied while the asset's operating profile says the asset is operating. When `ratePerHour` is omitted, the rate comes from the unit and the profile:

| Unit | Rate per operating hour |
| --- | --- |
| `h`, `hr`, `hour`, `hours` | `1` |
| `km` (or anything containing `kilomet`) | `avgSpeedKmh` (default 40) |
| `kwh` | `ratedKw` (default 10) |
| `t`, `ton`, `tons` | `tonsPerHour` (default 1) |
| `cycle`, `cycles` | `cyclesPerHour` (default 60) |
| anything else | `1` |

The legacy `incrementPerTick` field is still accepted by the API for backwards compatibility but is ignored on tick.

### `LEVEL`

`LEVEL` means the value usually decreases or increases in one direction depending on equipment behavior.

Examples:

- fuel level
- oil volume
- water tank level

Example behavior:

```text
fuel_level_liters: 220 -> 219.8 -> 219.6 -> 219.4
```

Recommended future shape:

```json
{
  "name": "fuel_level_liters",
  "unit": "L",
  "kind": "LEVEL",
  "min": 0,
  "max": 500,
  "drift": -0.2
}
```

## Target Integration Flow with Java Backend and React Frontend

The expected business architecture is:

```text
React Frontend
  -> user creates assets and counters/sensors
  -> user clicks "Simulate" for one counter/sensor

Java Backend
  -> main production backend
  -> stores real business entities, asset ownership, counters, permissions
  -> may call or coordinate with Go simulator

Go Simulator Backend
  -> hardware simulator only
  -> communicates over WebSocket
  -> generates fake telemetry/counter values
```

Example user flow:

```text
1. User creates Asset in React UI.
2. Java backend stores Asset, Sensors, Counters.
3. User adds counter: engine_hours / motochas.
4. User clicks "Simulate" on that counter.
5. React opens a modal or separate simulation page.
6. That page connects to Go simulator using WebSocket.
7. React sends simulation start command with assetId, metricName, kind, and current value.
8. Go simulator sends live counter values back over WebSocket.
9. React displays the running simulation.
10. Java backend can optionally receive/persist simulated values.
```

Recommended future WebSocket action:

```json
{
  "requestId": "sim-1",
  "action": "simulation.start",
  "payload": {
    "assetId": "EXC-1001",
    "metricName": "engine_hours",
    "unit": "h",
    "kind": "COUNTER",
    "initialValue": 1240.5,
    "incrementPerSecond": 0.000277
  }
}
```

Expected live event:

```json
{
  "type": "event",
  "ok": true,
  "data": {
    "event": "simulation.tick",
    "assetId": "EXC-1001",
    "metricName": "engine_hours",
    "kind": "COUNTER",
    "value": 1240.501,
    "unit": "h",
    "sentAt": "2026-08-03T09:00:00Z"
  }
}
```

Important: this `simulation.start` action is still a recommended future API shape for independent per-counter sessions. The current WebSocket implementation supports asset-level snapshot streaming, asset registration, asset type creation, and fault actions. Counter behavior already works when the metric is part of an asset type with `kind: "COUNTER"`.

## Architecture

- Go `net/http` router
- GORM + PostgreSQL persistence
- In-memory digital twin state protected by `sync.RWMutex`
- Background tick engine updates telemetry every `TICK_INTERVAL`
- REST API for standard CRUD-style commands
- WebSocket API for realtime control and telemetry streaming
- Optional outbound webhook push mode

## Configuration

Create `.env` in the backend project root:

```env
DB_HOST=localhost
DB_PORT=5432
DB_USER=postgres
DB_PASSWORD=linux
DB_NAME=telemetry
DB_SSL=disable
```

Runtime environment variables:

| Variable | Default | Description |
| --- | --- | --- |
| `PORT` | `8080` | HTTP and WebSocket server port |
| `TICK_INTERVAL` | `2s` | Simulation update interval |
| `PUSH_MODE` | `false` | Enables outbound telemetry POST push |
| `TARGET_TOIR_URL` | empty | Target URL when push mode is enabled |
| `TOIR_TELEMETRY_INGEST_SECRET` | empty | Shared secret sent as `X-Toir-Telemetry-Secret` |
| `PUSH_INTERVAL` | `TICK_INTERVAL` | Outbound push interval |

## Run

```bash
cd ~/GolandProjects/equipment-telemetry-simulator
PORT=8080 TICK_INTERVAL=2s go run -buildvcs=false ./cmd/server
```

LAN example:

```text
http://192.168.0.190:8080
ws://192.168.0.190:8080/api/v1/ws
```

## REST API

Base URL:

```text
http://localhost:8080/api/v1
```

### Health Check

```http
GET /healthz
```

Checks whether the backend process is running.

Example:

```bash
curl -s http://localhost:8080/healthz
```

### List Asset Types

```http
GET /api/v1/asset-types
```

Returns all equipment templates. Each template contains the sensor/metric schema and allowed faults.

Example:

```bash
curl -s http://localhost:8080/api/v1/asset-types | jq
```

### Create Asset Type

```http
POST /api/v1/asset-types
```

Creates a dynamic equipment template.

Example compressor with multiple sensors:

```bash
curl -s -X POST http://localhost:8080/api/v1/asset-types \
  -H "Content-Type: application/json" \
  -d '{
    "id": "COMPRESSOR_ADVANCED",
    "name": "Advanced Air Compressor",
    "description": "Compressor with air pressure, temperature, volume, oil volume, and engine hours",
    "metrics": [
      { "name": "temperature_c", "unit": "C", "kind": "GAUGE", "min": 60, "max": 85, "drift": 1.5 },
      { "name": "air_pressure_psi", "unit": "psi", "kind": "GAUGE", "min": 90, "max": 120, "drift": 2.5 },
      { "name": "air_volume_m3_min", "unit": "m3/min", "kind": "GAUGE", "min": 8, "max": 14, "drift": 0.5 },
      { "name": "oil_volume_liters", "unit": "L", "kind": "LEVEL", "min": 12, "max": 22, "drift": 0.2 },
      { "name": "engine_hours", "unit": "h", "kind": "COUNTER", "min": 0, "max": 999999, "drift": 0.01, "initialValue": 1240.5, "ratePerHour": 1 }
    ],
    "faultTypes": ["OVERHEATING", "PRESSURE_DROP", "VOLUME_DROP", "OIL_LEAK"]
  }' | jq
```

Note: `engine_hours` is a monotonic counter when `kind` is `COUNTER`. It grows by `ratePerHour * elapsedHours` of wall-clock time, and only while the asset's operating profile marks it as operating. If `ratePerHour` is omitted, the rate is derived from the unit.

### List Assets

```http
GET /api/v1/assets
```

Returns all registered equipment instances with live telemetry.

Example:

```bash
curl -s http://localhost:8080/api/v1/assets | jq
```

Response shape:

```json
[
  {
    "assetId": "COMP-301",
    "assetTypeId": "COMPRESSOR",
    "status": "RUNNING",
    "metrics": {
      "temperature_c": { "value": 72.4, "unit": "C" },
      "pressure_psi": { "value": 104.2, "unit": "psi" },
      "volume_m3_min": { "value": 11.3, "unit": "m3/min" },
      "engine_hours": { "value": 1240.75, "unit": "h" }
    },
    "activeFaults": [],
    "updatedAt": "2026-08-03T09:00:00Z"
  }
]
```

### Register Asset

```http
POST /api/v1/assets
```

Creates a running equipment instance from an asset type template.

Example:

```bash
curl -s -X POST http://localhost:8080/api/v1/assets \
  -H "Content-Type: application/json" \
  -d '{
    "assetId": "COMP-401",
    "assetTypeId": "COMPRESSOR_ADVANCED",
    "operatingProfile": { "mode": "CONTINUOUS" }
  }' | jq
```

`operatingProfile` is optional; when omitted the asset defaults to `CONTINUOUS`.

### Operating Profiles

An operating profile decides whether counters grow at a given moment, so a truck that works one shift a day does not accumulate 24 hours of engine time per day.

| Mode | Meaning |
| --- | --- |
| `CONTINUOUS` | Operates around the clock (process equipment, pumps, compressors). |
| `SHIFT` | Operates only inside the configured weekday/time windows. |
| `OUT_OF_SERVICE` | Never operates; counters freeze at their last value. |

```http
PUT /api/v1/assets/{assetId}/operating-profile
```

```bash
curl -s -X PUT http://localhost:8080/api/v1/assets/COMP-401/operating-profile \
  -H "Content-Type: application/json" \
  -d '{
    "mode": "SHIFT",
    "timezone": "Asia/Tashkent",
    "startHour": 8,
    "hoursPerDay": 8,
    "daysOfWeek": [1, 2, 3, 4, 5, 6],
    "avgSpeedKmh": 40
  }' | jq
```

Profile fields:

| Field | Default | Meaning |
| --- | --- | --- |
| `mode` | `CONTINUOUS` | `CONTINUOUS`, `SHIFT`, or `OUT_OF_SERVICE`. |
| `timezone` | `Asia/Tashkent` | IANA zone used to evaluate the shift window. |
| `startHour` | `8` | Local hour the shift starts (0-23). |
| `hoursPerDay` | `8` | Shift length in hours; `24` means always on for the allowed days. |
| `daysOfWeek` | `[1,2,3,4,5,6]` | ISO weekdays, 1 = Monday ... 7 = Sunday. |
| `stoppedUntil` | none | While `now` is before this timestamp the asset does not operate in any mode. |
| `avgSpeedKmh` | `40` | Rate for `km` counters without `ratePerHour`. |
| `ratedKw` | `10` | Rate for `kwh` counters without `ratePerHour`. |
| `cyclesPerHour` | `60` | Rate for cycle counters without `ratePerHour`. |
| `tonsPerHour` | `1` | Rate for tonnage counters without `ratePerHour`. |

A shift that crosses midnight is supported: `startHour: 22` with `hoursPerDay: 8` runs 22:00-06:00.

### Start and Stop an Asset

```http
PUT /api/v1/assets/{assetId}/running
```

Stopping switches the asset to `OUT_OF_SERVICE` and remembers the previous profile; starting restores it. Counters keep their last value while stopped, which is what TOIR needs for odometer and engine-hour history.

```bash
curl -s -X PUT http://localhost:8080/api/v1/assets/COMP-401/running \
  -H "Content-Type: application/json" \
  -d '{ "running": false }' | jq

curl -s -X PUT http://localhost:8080/api/v1/assets/COMP-401/running \
  -H "Content-Type: application/json" \
  -d '{ "running": true }' | jq
```

### Replace Asset Faults

```http
PUT /api/v1/assets/{assetId}/faults
```

Replaces all active faults for one asset.

- If `faultTypes` is empty, faults are cleared and the asset returns to `RUNNING`.
- If `faultTypes` has values, the asset becomes `FAULT`.
- Fault names are validated against the asset type's allowed `faultTypes`.

Inject fault:

```bash
curl -s -X PUT http://localhost:8080/api/v1/assets/COMP-401/faults \
  -H "Content-Type: application/json" \
  -d '{ "faultTypes": ["OVERHEATING"] }' | jq
```

Clear faults:

```bash
curl -s -X PUT http://localhost:8080/api/v1/assets/COMP-401/faults \
  -H "Content-Type: application/json" \
  -d '{ "faultTypes": [] }' | jq
```

## WebSocket API

WebSocket endpoint:

```text
ws://localhost:8080/api/v1/ws
```

LAN example:

```text
ws://192.168.0.190:8080/api/v1/ws
```

Every request is a JSON message:

```json
{
  "requestId": "optional-client-id",
  "action": "assets.list",
  "payload": {}
}
```

Every command response is:

```json
{
  "requestId": "optional-client-id",
  "type": "response",
  "action": "assets.list",
  "ok": true,
  "data": {}
}
```

Errors:

```json
{
  "requestId": "optional-client-id",
  "type": "response",
  "action": "assets.list",
  "ok": false,
  "error": "error message"
}
```

### Supported WebSocket Actions

| Action | Purpose |
| --- | --- |
| `ping` | Connection test. Returns `pong`. |
| `asset_types.list` | Returns all asset type templates. |
| `asset_types.create` | Creates a new asset type template. |
| `assets.list` | Returns all running asset instances with current telemetry. |
| `assets.register` | Registers a new asset instance from an asset type. |
| `assets.faults.replace` | Replaces active faults for an asset. |
| `assets.running.set` | Starts or stops an asset (`{ "assetId": "...", "running": false }`). |
| `assets.subscribe` | Starts realtime `assets.snapshot` events every 2 seconds. |
| `assets.unsubscribe` | Stops realtime asset snapshot events. |

### WebSocket Examples

#### List Assets

```json
{
  "requestId": "1",
  "action": "assets.list"
}
```

#### Subscribe to Live Telemetry

```json
{
  "requestId": "2",
  "action": "assets.subscribe"
}
```

After subscribing, the server sends events:

```json
{
  "type": "event",
  "ok": true,
  "data": {
    "event": "assets.snapshot",
    "assets": [
      {
        "assetId": "COMP-301",
        "assetTypeId": "COMPRESSOR",
        "status": "RUNNING",
        "metrics": {
          "temperature_c": { "value": 72.4, "unit": "C" },
          "pressure_psi": { "value": 104.2, "unit": "psi" }
        },
        "activeFaults": []
      }
    ],
    "sentAt": "2026-08-03T09:00:00Z"
  }
}
```

#### Create Asset Type

```json
{
  "requestId": "3",
  "action": "asset_types.create",
  "payload": {
    "id": "COMPRESSOR_ADVANCED",
    "name": "Advanced Air Compressor",
    "description": "Compressor with pressure, temperature, volume, and oil volume sensors",
    "metrics": [
      { "name": "temperature_c", "unit": "C", "min": 60, "max": 85, "drift": 1.5 },
      { "name": "air_pressure_psi", "unit": "psi", "min": 90, "max": 120, "drift": 2.5 },
      { "name": "air_volume_m3_min", "unit": "m3/min", "min": 8, "max": 14, "drift": 0.5 },
      { "name": "oil_volume_liters", "unit": "L", "min": 12, "max": 22, "drift": 0.2 }
    ],
    "faultTypes": ["OVERHEATING", "PRESSURE_DROP", "VOLUME_DROP", "OIL_LEAK"]
  }
}
```

#### Register Asset

```json
{
  "requestId": "4",
  "action": "assets.register",
  "payload": {
    "assetId": "COMP-401",
    "assetTypeId": "COMPRESSOR_ADVANCED"
  }
}
```

#### Inject Fault

```json
{
  "requestId": "5",
  "action": "assets.faults.replace",
  "payload": {
    "assetId": "COMP-401",
    "faultTypes": ["OVERHEATING"]
  }
}
```

#### Clear Faults

```json
{
  "requestId": "6",
  "action": "assets.faults.replace",
  "payload": {
    "assetId": "COMP-401",
    "faultTypes": []
  }
}
```

## Quick WebSocket Test with Node.js

Node 22 has a built-in WebSocket client:

```bash
node - <<'NODE'
const ws = new WebSocket("ws://localhost:8080/api/v1/ws");

ws.onmessage = (event) => {
  const msg = JSON.parse(event.data);
  console.log(JSON.stringify(msg, null, 2));

  if (msg.type === "connected") {
    ws.send(JSON.stringify({ requestId: "1", action: "assets.list" }));
    ws.send(JSON.stringify({ requestId: "2", action: "assets.subscribe" }));
  }

  if (msg.data?.event === "assets.snapshot") {
    ws.close();
  }
};
NODE
```

## Seeded Defaults

On startup the server seeds:

- `WATER_PUMP`
  - `temperature_c`
  - `pressure_bar`
- `COMPRESSOR`
  - `temperature_c`
  - `pressure_psi`
  - `volume_m3_min`

Seeded assets:

- `PUMP-101`
- `PUMP-102`
- `COMP-301`

## Counter Model

```text
MetricDefinition:
  - name
  - unit
  - kind
  - min
  - max
  - drift
  - initialValue
  - ratePerHour        (real-time rate; falls back to a unit-based default)
  - incrementPerTick   (legacy, accepted but ignored on tick)
```

Tick behavior, where `elapsedHours` is the real time since the previous tick and `operating` comes from the asset's operating profile:

```text
drift   = definition.drift, or (max - min) * 0.05 when drift is not set

GAUGE   -> newValue = oldValue + random(-drift, +drift)
           if it leaves [min, max] it is re-seeded randomly inside [min, max]
COUNTER -> newValue = oldValue + rate * elapsedHours          (only while operating, 6 decimals)
LEVEL   -> newValue = oldValue - (ratePerHour or |drift|) * elapsedHours
                                                              (only while operating, floored at min)
```

`elapsedHours` is measured from the previous tick and is capped at 30 seconds, so a simulator that was down for an hour does not jump the counters by an hour on the next tick.

While the asset is not operating (off-shift, stopped, or `OUT_OF_SERVICE`), counters and levels keep their last value, so the value pushed to TOIR stays stable instead of drifting.

Gauges are good for temperature, pressure, vibration, flow, and volume rate. Counters model odometers, engine hours, energy, and cycle counts. Levels model fuel and other tanks that drain while the asset works.

## Important Design Note

The simulator does not model sensors as separate top-level database records yet. Instead:

```text
Asset Type -> contains many MetricDefinition records in JSONB
Asset      -> contains current metric/sensor/counter values in JSONB
```

So your concept is already represented:

```text
Compressor asset
  -> air pressure sensor
  -> temperature sensor
  -> volume sensor
  -> oil volume sensor
  -> engine hours counter
```

In code/API these sensors and counters are called `metrics`. This keeps the simulator generic and lets every asset type define any number of sensors/counters dynamically without hardcoded Go structs.

## Production Notes

- Restrict WebSocket `CheckOrigin` before exposing publicly.
- Put the service behind HTTPS/WSS in production.
- Add authentication before connecting it to real EAM/TOiR workflows.
- Consider a dedicated `sensors` table later if sensors need IDs, calibration metadata, installation history, or per-sensor lifecycle management.
- Consider a dedicated `simulation_sessions` table later if frontend users can start/stop independent simulations per sensor/counter.
