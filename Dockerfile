# syntax=docker/dockerfile:1

FROM golang:1.24-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/gateway ./cmd/gateway

FROM alpine:3.20

RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/gateway /app/gateway
RUN mkdir -p /app/logs

EXPOSE 8080
CMD ["/app/gateway", "-config", "/app/config.yaml"]
