FROM golang:1.25.0-alpine3.22 AS build
WORKDIR /src
COPY agent/go.mod agent/go.sum ./
RUN go mod download
COPY agent/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mesh-relay ./cmd/mesh-relay

FROM alpine:3.22
RUN apk add --no-cache ca-certificates \
    && addgroup -g 65532 mesh \
    && adduser -D -H -u 65532 -G mesh mesh
COPY --from=build /out/mesh-relay /usr/local/bin/mesh-relay
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/mesh-relay"]
