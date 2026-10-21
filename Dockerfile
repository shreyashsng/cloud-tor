# Multi-stage build for ultra-lightweight, zero-overhead Go container
FROM golang:alpine AS builder

WORKDIR /app

RUN apk --no-cache add ca-certificates git

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o cloud-tor .

# Production runtime container
FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata mailcap
RUN mkdir -p /app/cache

WORKDIR /app

COPY --from=builder /app/cloud-tor .

ENV DATA_DIR=/app/cache
ENV PORT=3000
EXPOSE 3000

CMD ["./cloud-tor"]
