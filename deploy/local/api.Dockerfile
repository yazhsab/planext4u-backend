# syntax=docker/dockerfile:1.18
FROM golang:1.26.10-alpine3.23 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    for service in identity configuration catalog transaction admin gateway media migrate secret-bootstrap local-media-init; do \
      CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -ldflags="-s -w" \
        -o /out/${service} ./cmd/${service} || exit 1; \
    done

FROM alpine:3.23.5
RUN apk add --no-cache ca-certificates curl
WORKDIR /app
COPY --from=build /out/ /app/
COPY --from=build /src/migrations /app/migrations
# The existing bootstrap materializes *_FILE_VALUE inputs in a private tmpfs,
# then drops to uid/gid 65532 before executing the selected service.
ENTRYPOINT ["/app/secret-bootstrap"]
