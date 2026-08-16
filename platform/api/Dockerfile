# Must satisfy the go directive in go.mod, which is 1.25.0. A lower toolchain
# refuses the module outright.
FROM golang:1.26-alpine AS builder

WORKDIR /build

RUN apk add --no-cache git gcc musl-dev

COPY go.mod go.sum ./

RUN go mod download

COPY . .

RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-w -s" -o api ./cmd/api/main.go

FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /app

COPY --from=builder /build/api .

EXPOSE 8000

HEALTHCHECK --interval=10s --timeout=5s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8000/healthz || exit 1

ENTRYPOINT ["/app/api"]
