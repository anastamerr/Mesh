# syntax=docker/dockerfile:1
FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY agent/go.mod agent/go.sum ./
RUN go mod download
COPY agent/ ./
RUN CGO_ENABLED=0 go build -trimpath -o /mesh-executor ./cmd/mesh-executor

FROM ubuntu:24.04
ARG DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
    && apt-get install --yes --no-install-recommends ca-certificates docker.io iptables systemd systemd-sysv \
    && rm -rf /var/lib/apt/lists/* /var/lib/docker/* \
    && mkdir -p /etc/docker /var/lib/docker \
    && printf '%s\n' '{"icc":false,"live-restore":true,"log-driver":"local","log-opts":{"max-size":"10m","max-file":"3"}}' > /etc/docker/daemon.json \
    && printf '%s\n' '[boot]' 'systemd=true' '[user]' 'default=root' > /etc/wsl.conf \
    && systemctl enable docker.service containerd.service \
    && truncate --size 0 /etc/machine-id
COPY --from=build /mesh-executor /usr/local/bin/mesh-executor
RUN chmod 0755 /usr/local/bin/mesh-executor
