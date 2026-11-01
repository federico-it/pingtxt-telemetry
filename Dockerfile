# pingtxt-telemetry: one static Go binary on an empty image.

# Keep in sync with the "go" line of go.mod (CI checks it).
ARG GO_VERSION=1.26.10

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
ARG TARGETOS TARGETARCH
ENV CGO_ENABLED=0
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false \
      -ldflags "-s -w -X main.version=$VERSION" -o /out/pingtxt-telemetry ./cmd/pingtxt-telemetry \
 && mkdir -p /out/data

FROM scratch
COPY --from=build /out/pingtxt-telemetry /pingtxt-telemetry
# A new volume mounted on /data takes this owner, so uid 10001 can write it.
COPY --from=build --chown=10001:10001 /out/data /data
# No shell, no package manager, no root: uid/gid 10001 like the pingtxt images.
USER 10001:10001
ENV TELEMETRY_ADDR=:8080 TELEMETRY_DATA=/data
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD ["/pingtxt-telemetry", "healthcheck"]
ENTRYPOINT ["/pingtxt-telemetry"]
