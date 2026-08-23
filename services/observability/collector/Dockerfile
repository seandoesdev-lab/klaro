# Builds the klaro ingest gateway: a custom OpenTelemetry Collector
# distribution (see builder-config.yaml for why one is needed).
FROM golang:1.25 AS build

ARG OTELCOL_VERSION=0.159.0
WORKDIR /work/collector
COPY . /work/collector

# ocb generates a main package under ./_build and compiles it. The replaces
# entry in the manifest points at this directory, so the custom components are
# taken from the source copied above rather than from a module proxy.
RUN CGO_ENABLED=0 go run go.opentelemetry.io/collector/cmd/builder@v${OTELCOL_VERSION} \
      --config builder-config.yaml --skip-strict-versioning

FROM alpine:3.22
RUN adduser -D -u 10001 otel
COPY --from=build /work/collector/_build/klaro-otelcol /usr/local/bin/klaro-otelcol
USER otel
ENTRYPOINT ["/usr/local/bin/klaro-otelcol"]
CMD ["--config", "/etc/klaro/otel-collector.yaml"]
