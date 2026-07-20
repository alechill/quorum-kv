FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -o /out/server ./cmd/server

FROM alpine:3.20
# iptables lets the harness impose real network-level partitions inside the
# container (requires NET_ADMIN, granted only in the compose file).
RUN apk add --no-cache iptables ca-certificates
COPY --from=build /out/server /server
COPY docker-entrypoint.sh /docker-entrypoint.sh
RUN chmod +x /docker-entrypoint.sh
VOLUME /data
EXPOSE 8080 7000
ENTRYPOINT ["/docker-entrypoint.sh"]
