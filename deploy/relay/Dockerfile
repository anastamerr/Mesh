# syntax=docker/dockerfile:1
FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY agent/go.mod agent/go.sum ./
RUN go mod download
COPY agent/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /mesh-relay ./cmd/mesh-relay

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /mesh-relay /usr/local/bin/mesh-relay
ENTRYPOINT ["/usr/local/bin/mesh-relay"]
