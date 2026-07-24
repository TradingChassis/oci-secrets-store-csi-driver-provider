#
# OCI Secrets Store CSI Driver Provider
#
# Copyright (c) 2022 Oracle America, Inc. and its affiliates.
# Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
#

FROM golang:1.26 AS build

ARG TARGETOS=linux
ARG TARGETARCH

WORKDIR /provider-src

# Copy provider sources
COPY go.mod go.sum Makefile  ./
COPY cmd       ./cmd
COPY internal  ./internal
COPY vendor    ./vendor

# Cross-compile for TARGETARCH. Unit tests run in CI (ci.yaml), not in the image build,
# so multi-arch builds stay reliable and avoid slow QEMU test execution.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} make build

FROM oraclelinux:9-slim

ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown

LABEL author="OKE Foundations Team"
LABEL org.opencontainers.image.title="oci-secrets-store-csi-driver-provider"
LABEL org.opencontainers.image.description="OCI Vault provider for the Secrets Store CSI driver"
LABEL org.opencontainers.image.source="https://github.com/TradingChassis/oci-secrets-store-csi-driver-provider"
LABEL org.opencontainers.image.url="https://github.com/TradingChassis/oci-secrets-store-csi-driver-provider"
LABEL org.opencontainers.image.documentation="https://github.com/TradingChassis/oci-secrets-store-csi-driver-provider/blob/main/docs/TRADINGCHASSIS.md"
LABEL org.opencontainers.image.licenses="UPL-1.0"
LABEL org.opencontainers.image.vendor="TradingChassis"
LABEL org.opencontainers.image.version="${VERSION}"
LABEL org.opencontainers.image.revision="${REVISION}"
LABEL org.opencontainers.image.created="${CREATED}"

COPY --from=build /provider-src/dist/provider /opt/provider/bin/provider

# copy license files
COPY LICENSE.txt /opt/provider/licenses/LICENSE.txt
COPY THIRD_PARTY_LICENSES.txt /opt/provider/licenses/THIRD_PARTY_LICENSES.txt

# Note that the provider container should run as root in order to
# mount the Kubernetes hostPath volume and create Unix Domain Socket in that volume.

WORKDIR /opt/provider

ENTRYPOINT ["/opt/provider/bin/provider"]
