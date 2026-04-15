# ---------- Build stage ----------
FROM golang:1.26-alpine AS builder

WORKDIR /app

# Cache dependency downloads.
COPY go.mod go.sum ./
RUN go mod download

# Build a statically-linked binary.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o server .

# ---------- Runtime stage ----------
FROM alpine:3.21

# ca-certificates for outbound TLS (Google OAuth, PostgreSQL with SSL).
RUN apk add --no-cache ca-certificates

WORKDIR /app
COPY --from=builder /app/server .

EXPOSE 8080

ENTRYPOINT ["./server"]
