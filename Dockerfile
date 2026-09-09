# syntax=docker/dockerfile:1
FROM node:24-alpine AS web
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci --ignore-scripts
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/web/dist ./internal/web/dist
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH:-amd64} go build -trimpath -ldflags='-s -w' -o /out/upstream-pilot ./cmd/upstream-pilot

FROM alpine:3.22
RUN addgroup -S pilot && adduser -S -G pilot pilot \
  && mkdir -p /var/lib/upstream-pilot/logs \
  && chown -R pilot:pilot /var/lib/upstream-pilot
WORKDIR /opt/upstream-pilot
COPY --from=build /out/upstream-pilot /opt/upstream-pilot/upstream-pilot
USER pilot
ENV PILOT_LISTEN_ADDR=0.0.0.0:33777 \
    PILOT_LOG_DIR=/var/lib/upstream-pilot/logs
EXPOSE 33777
VOLUME ["/var/lib/upstream-pilot"]
ENTRYPOINT ["/opt/upstream-pilot/upstream-pilot"]
