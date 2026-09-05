# syntax=docker/dockerfile:1.7
# Single, pinned image shared by both benchmark harnesses.  Build-time network
# access is intentional; an attempt never installs software.
FROM node:22.19.0-bookworm AS dsh-build
ARG DSH_COMMIT=0a53fb55bea101816fa226bb964ae2bed71c343b
RUN apt-get update && apt-get install -y --no-install-recommends git python3 make g++ && rm -rf /var/lib/apt/lists/*
RUN corepack enable && corepack prepare pnpm@11.7.0 --activate
RUN git clone https://github.com/deepseek-ai/deepseek-harness.git /src && cd /src && git checkout --detach "$DSH_COMMIT" && test "$(git rev-parse HEAD)" = "$DSH_COMMIT"
WORKDIR /src
RUN pnpm install --frozen-lockfile && pnpm build

FROM node:22.19.0-bookworm-slim
ARG CODEX_VERSION=0.151.0
ARG DSH_COMMIT=0a53fb55bea101816fa226bb964ae2bed71c343b
RUN apt-get update && apt-get install -y --no-install-recommends python3 coreutils ca-certificates && rm -rf /var/lib/apt/lists/* \
 && corepack enable && corepack prepare pnpm@11.7.0 --activate \
 && npm install --global --ignore-scripts @openai/codex@${CODEX_VERSION} \
 && useradd --create-home --uid 10001 --shell /usr/sbin/nologin bench
COPY --from=dsh-build /src /opt/dsh
COPY docker/adapters/ /opt/bench/
COPY docker/dsh-runtime.sh /usr/local/bin/dsh
RUN ln -s /opt/dsh/apps/cli/lib/bin.js /usr/local/bin/dsh-real \
 && mkdir -p /opt/dsh/node_modules/@deepseek-ai \
 && for package in /opt/dsh/node_modules/.pnpm/node_modules/@deepseek-ai/*; do \
      name="$(basename "$package")"; \
      target="/opt/dsh/node_modules/@deepseek-ai/$name"; \
      if [ ! -e "$target" ] && [ ! -L "$target" ]; then \
        ln -s "../.pnpm/node_modules/@deepseek-ai/$name" "$target"; \
      fi; \
    done \
 && chmod 0555 /usr/local/bin/dsh \
 && find /opt/bench -type f -name '*.sh' -exec chmod 0555 {} + \
 && chmod -R a-w /opt/bench /opt/dsh
ENV DSH_HOME=/home/bench/.dsh HOME=/home/bench PATH=/usr/local/bin:/opt/dsh/apps/cli/bin:/usr/bin:/bin PYTHONDONTWRITEBYTECODE=1
USER bench
WORKDIR /workspace
LABEL org.opencontainers.image.title="agent-harness-benchmark-agent" \
      org.opencontainers.image.version="codex-0.151.0-dsh-0.1.2-alpha.2" \
      org.opencontainers.image.revision="0a53fb55bea101816fa226bb964ae2bed71c343b"
