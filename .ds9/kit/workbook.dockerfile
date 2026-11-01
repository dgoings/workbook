# syntax=docker/dockerfile:1
FROM debian:trixie-slim AS build
ARG WORKBOOK_VERSION
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates
RUN case "$TARGETARCH" in \
      amd64|arm64) ;; \
      *) echo "unsupported TARGETARCH: $TARGETARCH" >&2; exit 1 ;; \
    esac

# Workbook publishes static Go binaries per platform; verify against the
# release's checksums.txt rather than trusting the download alone.
WORKDIR /tmp/wb
RUN base="https://github.com/dgoings/workbook/releases/download/v${WORKBOOK_VERSION}" \
 && archive="workbook_${WORKBOOK_VERSION}_linux_${TARGETARCH}.tar.gz" \
 && curl -fsSLO "$base/$archive" \
 && curl -fsSLO "$base/checksums.txt" \
 && grep " ${archive}\$" checksums.txt | sha256sum -c - \
 && tar xzf "$archive" workbook \
 && install -D -m 0755 workbook /out/usr/local/bin/workbook

FROM scratch
COPY --from=build /out /
