# syntax=docker/dockerfile:1.18
FROM golang:1.27.1-alpine3.23 AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev
ARG COMMIT=unknown
ARG SERVICE_COMMAND=platform
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    case "${SERVICE_COMMAND}" in platform|staging-slice|gateway|identity|configuration|catalog|transaction|admin|customer-web|audit|media|messaging|notification) ;; *) exit 2 ;; esac && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /out/planext4u ./cmd/${SERVICE_COMMAND}
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/secret-bootstrap ./cmd/secret-bootstrap
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/migrate ./cmd/migrate

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=65532:65532 /out/planext4u /planext4u
COPY --from=build --chown=65532:65532 /out/secret-bootstrap /secret-bootstrap
COPY --from=build --chown=65532:65532 /out/migrate /migrate
COPY --from=build --chown=65532:65532 /src/migrations /migrations
USER 0:0
EXPOSE 8080
ENTRYPOINT ["/secret-bootstrap"]
CMD ["/planext4u"]
