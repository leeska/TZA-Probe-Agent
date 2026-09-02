FROM alpine:3.21

WORKDIR /app

# Docker buildx 会在构建时自动填充这些变量
ARG TARGETOS
ARG TARGETARCH
ARG NEXTTRACE_VERSION=v1.7.3

# NextTrace supplies per-hop ASN evidence for route classification. The
# traditional traceroute package remains as the compatibility fallback.
RUN apk add --no-cache ca-certificates curl traceroute \
    && curl -fL "https://github.com/nxtrace/NTrace-core/releases/download/${NEXTTRACE_VERSION}/nexttrace_linux_${TARGETARCH}" -o /app/nexttrace \
    && curl -fL "https://github.com/nxtrace/NTrace-core/releases/download/${NEXTTRACE_VERSION}/nexttrace-tiny_linux_${TARGETARCH}" -o /app/nexttrace-tiny \
    && chmod 755 /app/nexttrace-tiny \
    && chmod 755 /app/nexttrace

COPY --chmod=755 tza-probe-agent-${TARGETOS}-${TARGETARCH} /app/tza-probe-agent

RUN touch /.tza-probe-agent-container

ENTRYPOINT ["/app/tza-probe-agent"]
# 运行时请指定参数
# Please specify parameters at runtime.
# eg: docker run tza-probe-agent -e example.com -t token
CMD ["--help"]
