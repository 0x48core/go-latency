FROM golang:1.25-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o /cache-stampede-app ./cmd/main.go

# ── Runtime image ────────────────────────────────────────────────────────────
FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /cache-stampede-app /usr/local/bin/cache-stampede-app
EXPOSE 8080 9090
ENTRYPOINT ["cache-stampede-app"]
