# The train-ticket-k8s task image: every job of the task runs in it, and so do its pods in
# the cluster (the seed, probe and phase pods), which run this image rebuilt in-cluster from
# the task directory.
#
# It holds vibesys-evaluator at the release VIBESYS_EVALUATOR_VERSION (the one pin of this
# file; the Go SDK's tag, which pip reads as the same PEP 440 version, v0.2.0-rc.1 being
# 0.2.0rc1): the Python package from PyPI, loadgen and otelcapture (go install of the loadgen
# module), plus kubectl and buildctl; and the task's Go programs from its evaluator/ module,
# whose go.mod requires the loadgen module (the Go SDK) at the same release
# (tests/test_pins.py checks they agree): tt-generator (the benchmark's request generator and
# checks) and tt-seed (creates the fixture through the public API in the seed Job).
#
# The interim accuracy check (check.py) runs the servicebench Train Ticket accuracy suite,
# built from ORACLE_VIBESYS_COMMIT, the last vibesys commit that has servicebench
# (resources/evaluators/microservice). See README.md.

ARG VIBESYS_EVALUATOR_VERSION=v0.2.0-rc.1
ARG ORACLE_VIBESYS_COMMIT=5b13578e0d29daf7b2eb3c5f9a888a36863e476b
ARG KUBECTL_VERSION=v1.34.0
ARG KUBECTL_SHA256=cfda68cba5848bc3b6c6135ae2f20ba2c78de20059f68789c090166d6abc3e2c

FROM golang:1.25.9-bookworm@sha256:298734aec230b5f3e8cee450ce6d7eccc39f1797ba548ee90d57e9803030c6c3 AS go
ARG VIBESYS_EVALUATOR_VERSION
ARG KUBECTL_VERSION
ARG KUBECTL_SHA256
ENV CGO_ENABLED=0 GOFLAGS=-trimpath GOBIN=/out
RUN go install \
      "github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/loadgen/cmd/loadgen@${VIBESYS_EVALUATOR_VERSION}" \
      "github.com/uw-syfi/vibesys/sdk/vibesys-evaluator/loadgen/cmd/otelcapture@${VIBESYS_EVALUATOR_VERSION}"
RUN curl -fsSL -o /out/kubectl "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" \
 && echo "${KUBECTL_SHA256}  /out/kubectl" | sha256sum -c - \
 && chmod 0755 /out/kubectl
COPY evaluator/ /build/evaluator/
RUN cd /build/evaluator \
 && go build -o /out/tt-generator ./cmd/generator \
 && go build -o /out/tt-seed ./cmd/seed

FROM golang:1.25.9-bookworm@sha256:298734aec230b5f3e8cee450ce6d7eccc39f1797ba548ee90d57e9803030c6c3 AS oracle
ARG ORACLE_VIBESYS_COMMIT
RUN git init -q /src \
 && git -C /src fetch -q --depth 1 https://github.com/uw-syfi/vibesys.git "${ORACLE_VIBESYS_COMMIT}" \
 && git -C /src checkout -q FETCH_HEAD \
 && test "$(git -C /src rev-parse HEAD)" = "${ORACLE_VIBESYS_COMMIT}"
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
RUN cd /src/resources/evaluators/microservice && go build -o /out/servicebench ./cmd/servicebench

FROM moby/buildkit:v0.22.0@sha256:832fa7aa1eb3deb56fa62ae933bfa42dad9a83ff4824dbbaf173b49c722b59d0 AS buildkit

FROM python:3.12.12-slim-bookworm@sha256:593bd06efe90efa80dc4eee3948be7c0fde4134606dd40d8dd8dbcade98e669c
ARG VIBESYS_EVALUATOR_VERSION
RUN pip install --no-cache-dir "vibesys-evaluator==${VIBESYS_EVALUATOR_VERSION#v}"
COPY --from=go /out/loadgen /out/otelcapture /out/kubectl /out/tt-generator /out/tt-seed /usr/local/bin/
COPY --from=oracle /out/servicebench /usr/local/bin/
COPY --from=buildkit /usr/bin/buildctl /usr/local/bin/buildctl
# A job runs as the invoking user, whose HOME may not exist: kubectl and buildctl keep
# their caches under /tmp. Nothing writes into /task.
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 HOME=/tmp KUBECACHEDIR=/tmp/.kube/cache
COPY . /task
