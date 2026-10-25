FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/brother-scan-server ./cmd/brother-scan-server
RUN mkdir -p /out/data /out/scans

# Runs as root by default so bind-mounted ./data and ./scans are writable
# whatever their owner; set `user: "1000:1000"` in compose to drop privileges.
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/brother-scan-server /usr/local/bin/brother-scan-server
COPY --from=build /out/data /data
COPY --from=build /out/scans /scans
ENV CONFIG_PATH=/data/config.yaml
VOLUME ["/data"]
EXPOSE 8080/tcp 54925/udp
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/brother-scan-server", "-healthcheck"]
ENTRYPOINT ["/usr/local/bin/brother-scan-server"]
