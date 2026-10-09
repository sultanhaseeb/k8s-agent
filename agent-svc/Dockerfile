# The Claude CLI image from the repo root, plus kubectl and a small FastAPI
# wrapper so go-svc can hand it tasks over HTTP.
FROM node:22-bookworm-slim

ARG TARGETARCH
ARG KUBECTL_VERSION=v1.32.0

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
        less \
        python3 \
        python3-venv \
        ripgrep \
    && rm -rf /var/lib/apt/lists/*

RUN curl -fsSLo /usr/local/bin/kubectl \
        "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" \
    && chmod 0755 /usr/local/bin/kubectl \
    && kubectl version --client=true

RUN npm install -g @anthropic-ai/claude-code

# Debian's Python is externally managed, so the wrapper gets its own venv.
RUN python3 -m venv /opt/venv
COPY requirements.txt /app/requirements.txt
RUN /opt/venv/bin/pip install --no-cache-dir -r /app/requirements.txt

COPY app.py /app/app.py

# Login state lives here and is backed by a PVC so it survives restarts.
ENV CLAUDE_CONFIG_DIR=/home/node/.claude \
    PATH=/opt/venv/bin:$PATH \
    PYTHONUNBUFFERED=1

USER node
WORKDIR /app
EXPOSE 8080

CMD ["/opt/venv/bin/uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8080"]
