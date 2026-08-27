FROM golang:1.25.14-alpine3.23 AS build

WORKDIR /src
COPY go.mod ./
COPY cmd/fake-provider ./cmd/fake-provider
COPY internal/fakeprovider ./internal/fakeprovider
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/fake-provider ./cmd/fake-provider

FROM alpine:3.23.5

RUN addgroup -S planext4u && adduser -S -G planext4u -u 10001 planext4u
COPY --from=build /out/fake-provider /usr/local/bin/fake-provider
USER 10001
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/fake-provider"]
