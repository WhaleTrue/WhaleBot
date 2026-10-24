FROM node:24-alpine AS frontend
WORKDIR /web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/node ./cmd/node && \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/bootstrap ./cmd/bootstrap && \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/chat ./cmd/chat && \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/hello ./examples/hello

FROM alpine:3.23 AS bootstrap
COPY --from=build /out/bootstrap /usr/local/bin/bootstrap
ENTRYPOINT ["/bin/sh", "-ec"]
CMD ["bootstrap && chown -R 10001:10001 /identities"]

FROM alpine:3.23 AS node
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
RUN addgroup -g 10001 mesh && adduser -D -u 10001 -G mesh mesh && mkdir -p /data /app/web && chown -R mesh:mesh /data /app
WORKDIR /app
COPY --from=build /out/node /app/node
COPY --from=build /out/chat /app/chat
COPY --from=frontend /web/dist /app/web
USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=5s --timeout=3s --start-period=5s --retries=10 CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/app/node"]

FROM node AS hello
COPY --from=build /out/hello /app/hello
ENTRYPOINT ["/app/hello"]
