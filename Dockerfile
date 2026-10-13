# Net-Bond-007 server image. Runs unprivileged (no NET_ADMIN, no /dev/net/tun):
# client traffic is terminated in a userspace TCP/IP stack.
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
# Version comes from internal/version/VERSION (embedded); VERSION overrides.
ARG VERSION=
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w ${VERSION:+-X main.version=${VERSION}}" -o /out/net-bond-007-server ./cmd/net-bond-007-server

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && mkdir /data
COPY --from=build /out/net-bond-007-server /usr/local/bin/net-bond-007-server
VOLUME /data
# 7007/udp: bond traffic from clients. 8080/tcp: dashboard.
EXPOSE 7007/udp 8080/tcp
ENV NET_BOND_007_CONFIG=/data/server.json
ENTRYPOINT ["/usr/local/bin/net-bond-007-server"]
