# The train-ticket-k8s task image: every job of the task runs in it, and so do its pods in
# the cluster (the seed, probe and phase pods), which run this image rebuilt in-cluster from
# the task directory.
#
# It is vibesys-evaluator's microservices image at the release 0.3.0rc2 (the one pin of the
# evaluator in this file, by tag and digest): Python with the vibesys_evaluator package
# 0.3.0rc2 from PyPI, its bundled Go binaries (loadgen, otelcapture and the core's
# vibesys-evaluator) on the PATH, kubectl and buildctl. On it go the task's Go programs, built
# from its evaluator/ module, whose go.mod requires the Go SDK at the same release, v0.3.0-rc.2
# (tests/test_pins.py checks they agree): tt-generator (the benchmark's request generator),
# tt-seed (creates the fixture through the public API in the seed Job), and the accuracy
# check's tt-accuracy (its request generator) and tt-oracle (its oracle, on go/check).

FROM golang:1.25.9-bookworm@sha256:298734aec230b5f3e8cee450ce6d7eccc39f1797ba548ee90d57e9803030c6c3 AS go
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY evaluator/go.mod evaluator/go.sum /build/evaluator/
RUN cd /build/evaluator && go mod download
COPY evaluator/ /build/evaluator/
RUN cd /build/evaluator \
 && go build -o /out/tt-generator ./cmd/generator \
 && go build -o /out/tt-seed ./cmd/seed \
 && go build -o /out/tt-accuracy ./cmd/accuracy \
 && go build -o /out/tt-oracle ./cmd/oracle

FROM ghcr.io/uw-syfi/vibesys-evaluator-microservices:0.3.0rc2@sha256:64dc4a0dfe63507da08080c6e8925e9e7622d268bf200b71c817821f00f1140e
COPY --from=go /out/tt-generator /out/tt-seed /out/tt-accuracy /out/tt-oracle /usr/local/bin/
COPY . /task
