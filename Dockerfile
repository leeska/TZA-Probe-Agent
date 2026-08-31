FROM alpine:3.21

WORKDIR /app

# Carrier return-route probes execute the bounded traceroute command from the
# agent. Keep it in the runtime image so Docker installations do not report
# every configured route as unsupported.
RUN apk add --no-cache traceroute

# Docker buildx 会在构建时自动填充这些变量
ARG TARGETOS
ARG TARGETARCH

COPY --chmod=755 tza-probe-agent-${TARGETOS}-${TARGETARCH} /app/tza-probe-agent

RUN touch /.tza-probe-agent-container

ENTRYPOINT ["/app/tza-probe-agent"]
# 运行时请指定参数
# Please specify parameters at runtime.
# eg: docker run tza-probe-agent -e example.com -t token
CMD ["--help"]
