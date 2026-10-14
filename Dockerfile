FROM --platform=$BUILDPLATFORM golang:1.27.2 AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY third_party/zlib ./third_party/zlib
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY third_party/licenses ./third_party/licenses
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /out/cwa-book-mcp ./cmd/cwa-book-mcp && mkdir -p /out/data/state /out/data/staging && chmod 700 /out/data/state /out/data/staging

FROM scratch
COPY --from=build /out/cwa-book-mcp /cwa-book-mcp
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=10001:10001 /out/data /data
COPY LICENSE /licenses/LICENSE
COPY third_party/licenses /licenses/dependencies
USER 10001:10001
ENV LISTEN_ADDR=:8080 STATE_DIR=/data/state STAGING_DIR=/data/staging GOMEMLIMIT=64MiB
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["/cwa-book-mcp", "--healthcheck"]
ENTRYPOINT ["/cwa-book-mcp"]
