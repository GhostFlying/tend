FROM node:24-bookworm-slim AS node

FROM golang:1.27-bookworm

ARG USERNAME=dev
ARG USER_UID=1000
ARG USER_GID=1000

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
       sqlite3 jq ripgrep less sudo ca-certificates bubblewrap \
    && rm -rf /var/lib/apt/lists/*

# Node 24 runtime for the web client toolchain.
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
    && npm install -g pnpm@10 \
    && npm cache clean --force

# Login shells reset PATH via /etc/profile; keep the Go toolchain on it.
RUN echo 'export PATH=/usr/local/go/bin:/go/bin:$PATH' > /etc/profile.d/go.sh

# traex is bind-mounted read-only from the host at /opt/traex.
RUN ln -s /opt/traex/current/traex /usr/local/bin/traex

RUN groupadd --gid "${USER_GID}" "${USERNAME}" \
    && useradd --uid "${USER_UID}" --gid "${USER_GID}" -m -s /bin/bash "${USERNAME}" \
    && echo "${USERNAME} ALL=(root) NOPASSWD:ALL" > "/etc/sudoers.d/${USERNAME}" \
    && chmod 0440 "/etc/sudoers.d/${USERNAME}" \
    && mkdir -p /go/pkg/mod "/home/${USERNAME}/.local/share/tend" "/home/${USERNAME}/.cache" \
    && chown -R "${USER_UID}:${USER_GID}" /go "/home/${USERNAME}"

USER ${USERNAME}
