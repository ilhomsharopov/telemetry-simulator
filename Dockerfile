FROM golang:1.25-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/equipment-telemetry-simulator ./cmd/server

FROM alpine:3.20

RUN apk add --no-cache ca-certificates
COPY --from=builder /out/equipment-telemetry-simulator /equipment-telemetry-simulator

EXPOSE 8080
ENTRYPOINT ["/equipment-telemetry-simulator"]
