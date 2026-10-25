# syntax=docker/dockerfile:1
# Integration-only tools. Build the existing official releases from pinned
# source because their former public container repositories reject pulls.
FROM golang:1.22.5-alpine@sha256:0d3653dd6f35159ec6e3d10263a42372f6f194c3dea0b35235d72aabde86486e AS build-base
ARG GOPROXY=https://proxy.golang.org,direct
RUN sed -i 's|dl-cdn.alpinelinux.org|mirrors.aliyun.com|g' /etc/apk/repositories \
    && apk add --no-cache ca-certificates git
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOMAXPROCS=4 GOFLAGS=-p=4 GOPROXY=${GOPROXY}

FROM build-base AS minio-build
ADD --checksum=sha256:b0223962d0d46fd0a98ec7c08faaa2b89a1947bf4fd8ce7ed1792dcdf82f6098 https://codeload.github.com/minio/minio/tar.gz/refs/tags/RELEASE.2024-08-03T04-33-23Z /tmp/source.tar.gz
WORKDIR /src
RUN tar -xzf /tmp/source.tar.gz --strip-components=1
RUN --mount=type=cache,target=/go/pkg/mod go build -trimpath -o /out/minio .

FROM build-base AS mc-build
ADD --checksum=sha256:75c84f51b5e7bb865b933996cb0f2fbd3966620bf46b08ce08706fe10058b62e https://codeload.github.com/minio/mc/tar.gz/refs/tags/RELEASE.2024-07-31T15-58-33Z /tmp/source.tar.gz
WORKDIR /src
RUN tar -xzf /tmp/source.tar.gz --strip-components=1
RUN --mount=type=cache,target=/go/pkg/mod go build -trimpath -o /out/mc .

FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc AS runtime
RUN sed -i 's|dl-cdn.alpinelinux.org|mirrors.aliyun.com|g' /etc/apk/repositories \
    && apk add --no-cache ca-certificates curl

FROM runtime AS minio
COPY --from=minio-build /out/minio /usr/bin/minio
COPY --from=minio-build /src/LICENSE /licenses/minio-LICENSE
ENTRYPOINT ["/usr/bin/minio"]

FROM runtime AS mc
COPY --from=mc-build /out/mc /usr/bin/mc
COPY --from=mc-build /src/LICENSE /licenses/mc-LICENSE
ENTRYPOINT ["/usr/bin/mc"]
