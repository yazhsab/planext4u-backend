FROM golang:1.26.11-alpine3.23 AS tooling
# The infrastructure tests execute Bash scripts with stubbed AWS commands and
# need jq/curl; gcc/musl support the Go race detector. No cloud credentials.
RUN apk add --no-cache nodejs npm make gcc musl-dev git bash jq curl
ENV GOMAXPROCS=4
WORKDIR /src

FROM tooling AS verification
# The Dockerfile-specific ignore file excludes local profiles, secrets, caches
# and Git configuration. Tests execute this source snapshot, not a host mount.
COPY . /src
RUN find /src/scripts -type f -name '*.sh' -exec sed -i 's/\r$//' {} +
ENTRYPOINT ["sh", "/src/scripts/completion/verify-linux.sh"]
