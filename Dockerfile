# ── Stage 1: build ────────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copy the entire source including the vendor directory
COPY . .

# Build using vendored dependencies — no network access needed
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -ldflags="-s -w" -o server .

# ── Stage 2: runtime ──────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS runner

WORKDIR /app

COPY --from=builder /app/server .

EXPOSE 8080

ENTRYPOINT ["./server"]
