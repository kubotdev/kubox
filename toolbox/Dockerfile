# The official kubox toolbox image: what `kubox exec` runs your commands in.
# kubectl + k6 + kubot + shell tools, nothing else. Published multi-arch by
# .github/workflows/toolbox.yml to ghcr.io/kubotdev/kubox-toolbox.
FROM alpine:3.22

# Keep k6 in sync with internal/env/k6.go (K6Image) so ad-hoc toolbox runs
# behave exactly like `kubox test load`.
ARG KUBECTL_VERSION=v1.37.2
ARG K6_VERSION=v0.54.0
ARG KUBOT_VERSION=v0.4.0
# Set automatically by buildx (amd64/arm64 — same names the releases use).
ARG TARGETARCH

RUN apk add --no-cache \
      bash ca-certificates curl jq tzdata \
      bind-tools iproute2 util-linux procps tar gzip

RUN curl -fsSL -o /usr/local/bin/kubectl \
      "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" \
    && chmod +x /usr/local/bin/kubectl

RUN curl -fsSL \
      "https://github.com/grafana/k6/releases/download/${K6_VERSION}/k6-${K6_VERSION}-linux-${TARGETARCH}.tar.gz" \
    | tar -xz --strip-components=1 -C /usr/local/bin \
      "k6-${K6_VERSION}-linux-${TARGETARCH}/k6"

RUN curl -fsSL \
      "https://github.com/kubotdev/kubot/releases/download/${KUBOT_VERSION}/kubot_${KUBOT_VERSION#v}_linux_${TARGETARCH}.tar.gz" \
    | tar -xz -C /usr/local/bin kubot

# kubox's toolbox pod overrides the command with a sleep loop; keep a sane
# default for `docker run` too.
CMD ["sh", "-c", "while true; do sleep 3600; done"]
