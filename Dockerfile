# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -ldflags "-s -w -X main.version=$VERSION" -o /out/yarg-song-server ./cmd/yarg-song-server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/yarg-song-server /usr/local/bin/yarg-song-server
# /songs  - the library, mounted read-only in normal operation
# /data   - catalog and server state
VOLUME ["/songs", "/data"]
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/yarg-song-server"]
CMD ["--songs", "/songs", "--data", "/data", "--listen", ":8080"]
