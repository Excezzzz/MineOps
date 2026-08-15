# MineOps — Telegram bot for Aternos Minecraft server management (Go)
# Multi-stage: статический бинарник без CGO -> минимальный runtime-образ.

# ---------- stage 1: builder ----------
FROM golang:1.26-alpine AS builder

WORKDIR /src

# Кэш модулей отдельно (пока не изменились go.mod/go.sum).
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0: modernc.org/sqlite — чистый Go, линковка не нужна.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mineops ./cmd/bot

# ---------- stage 2: runtime ----------
FROM alpine:3.21

# ca-certificates: TLS до api.telegram.org / aternos.org / mcsrvstat.us.
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app
COPY --from=builder /out/mineops /app/mineops

ENV TZ=UTC

CMD ["/app/mineops"]
