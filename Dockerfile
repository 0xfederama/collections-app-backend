# Keep the Go tag in step with go.mod (DECISIONS "Toolchain versions").
# Build on the runner's native platform and cross-compile: no QEMU for the Go build.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine3.24 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
# db/ carries the migrations embedded in the binary (`/app/api migrate`).
COPY db ./db
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api \
 && mkdir /data

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/api /app/api
# Pre-create the data dir owned by the nonroot user (65532) so a fresh named volume inherits it.
COPY --from=build --chown=65532:65532 /data /data
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/app/api"]
