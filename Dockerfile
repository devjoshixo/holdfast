# Stage 1: Build the Go binary
FROM golang:alpine AS builder
WORKDIR /app

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code and build statically linked binary
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /app/bin/api ./cmd/api

# Stage 2: Minimal production runtime
FROM alpine:3.20
WORKDIR /app

# ca-certificates for TLS and tzdata for timezones
RUN apk --no-cache add ca-certificates tzdata

# Copy compiled binary and web assets from builder
COPY --from=builder /app/bin/api /app/bin/api
COPY --from=builder /app/web /app/web

ENV PORT=8080
EXPOSE 8080

ENTRYPOINT ["/app/bin/api"]
