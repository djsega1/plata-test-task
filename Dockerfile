# syntax=docker/dockerfile:1

# Builder: compiles a static binary. CGO disabled — pgx and every other
# dependency in go.mod are pure Go, so no C toolchain is needed at all.
FROM golang:1.27-alpine AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/server ./cmd/server

# Runtime: alpine, not distroless — a reviewer's smoke test may want a shell
# (docker compose exec) and CA certs matter here (PROVIDER=exchangeratedev
# talks HTTPS out); ca-certificates is the one package that buys that.
FROM alpine:3.21
RUN apk add --no-cache ca-certificates && \
    addgroup -S app && adduser -S app -G app
USER app

COPY --from=builder /out/server /usr/local/bin/server

EXPOSE 8080
ENTRYPOINT ["server"]
