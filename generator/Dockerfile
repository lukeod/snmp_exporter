FROM golang:bookworm AS builder

ARG REPO_TAG=main
RUN go install github.com/prometheus/snmp_exporter/generator@"$REPO_TAG"

FROM debian:bookworm-slim

WORKDIR "/opt"

ENTRYPOINT ["/bin/generator"]

ENV MIBDIRS=mibs

CMD ["generate"]

COPY --from=builder /go/bin/generator /bin/generator
