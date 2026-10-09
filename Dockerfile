FROM node:22-bookworm-slim

# Runtime deps Claude Code expects: git for repo ops, ripgrep for search,
# curl/ca-certificates for network calls, less for pager output.
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
        less \
        ripgrep \
    && rm -rf /var/lib/apt/lists/*

RUN npm install -g @anthropic-ai/claude-code

# Run as non-root: Claude Code refuses --dangerously-skip-permissions as root,
# and files it writes into the mounted workspace keep your host ownership.
ARG UID=1000
ARG GID=1000
RUN groupmod -g "${GID}" node 2>/dev/null || true \
    && usermod -u "${UID}" -g "${GID}" node 2>/dev/null || true \
    && mkdir -p /home/node/.claude /workspace \
    && chown -R node:node /home/node /workspace

USER node
WORKDIR /workspace

# Auth: mount a volume at /home/node/.claude to persist login across containers,
# or pass ANTHROPIC_API_KEY at run time.
ENV CLAUDE_CONFIG_DIR=/home/node/.claude

ENTRYPOINT ["claude"]
