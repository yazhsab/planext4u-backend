# The previous public minio/minio image cannot currently be pulled. Build the
# upstream source directly rather than depending on an unverified image mirror.
FROM golang:1.26.10-alpine3.23 AS build
RUN apk add --no-cache ca-certificates curl
WORKDIR /src
# Upstream release RELEASE.2025-10-15T17-29-55Z, resolved to an immutable commit.
RUN curl --fail --location --retry 3 --output /tmp/minio.tar.gz \
      https://codeload.github.com/minio/minio/tar.gz/9e49d5e7a648f00e26f2246f4dc28e6b07f8c84a && \
    echo "45521908307306e925c98d629e1c17d78c8b72b6ee242b1bfb1409f7d8ee5841  /tmp/minio.tar.gz" | sha256sum -c - && \
    tar -xzf /tmp/minio.tar.gz --strip-components=1 && \
    rm /tmp/minio.tar.gz
# Explicit dependency overlay for GO-2026-5841; the source archive above remains
# checksum-pinned. Go's checksum database verifies this exact module version.
RUN go get github.com/klauspost/compress@v1.18.7 && \
    CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/minio .

FROM alpine:3.23.5
LABEL org.opencontainers.image.source="https://github.com/minio/minio" \
      org.opencontainers.image.revision="9e49d5e7a648f00e26f2246f4dc28e6b07f8c84a" \
      org.opencontainers.image.version="RELEASE.2025-10-15T17-29-55Z" \
      net.planext4u.dependency-patches="github.com/klauspost/compress@v1.18.7"
RUN apk add --no-cache ca-certificates curl && \
    addgroup -S minio && adduser -S -G minio -u 10001 minio && \
    mkdir /data && chown minio:minio /data
COPY --from=build /out/minio /usr/local/bin/minio
COPY --from=build /src/LICENSE /usr/share/licenses/minio/LICENSE
USER 10001
EXPOSE 9000 9001
ENTRYPOINT ["/usr/local/bin/minio"]
