# SPDX-License-Identifier: Apache-2.0
# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build

WORKDIR /src

# Cache module downloads separately so source changes don't invalidate them.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags='-s -w' \
      -o /out/argocd-labels-groups-generator \
      ./cmd/argocd-labels-groups-generator

# Distroless runtime: no shell or package manager; includes CA certificates.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build --chown=65532:65532 \
    /out/argocd-labels-groups-generator \
    /argocd-labels-groups-generator

USER 65532:65532
EXPOSE 4355
STOPSIGNAL SIGTERM
ENTRYPOINT ["/argocd-labels-groups-generator"]
