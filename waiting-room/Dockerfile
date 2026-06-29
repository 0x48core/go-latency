FROM golang:1.25.1-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .

RUN go build -o main ./cmd/server/main.go

FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/main .

CMD ["/app/main"]
