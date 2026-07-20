FROM golang:1.25.5 AS builder

WORKDIR /app/ncli
COPY . .

RUN CGO_ENABLED=0 go build -o /go/bin/ncli ./cmd/ncli


# second stage

FROM alpine:3.16
LABEL org.opencontainers.image.source="https://github.com/ohstr/ncli"

COPY --from=builder /go/bin /bin

# Relay configs use relative paths (store: ./data/db/..., logdir: ./data/logs),
# so the working directory has to be where those are expected to land —
# matching the volume mounts in docker-compose.dev.yaml/prod.yaml.
WORKDIR /app/ncli

EXPOSE 5500

ENTRYPOINT ["/bin/ncli"]
