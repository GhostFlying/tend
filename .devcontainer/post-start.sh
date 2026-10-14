#!/usr/bin/env bash
# traex authenticates over SSH and needs a writable ~/.ssh for
# transient files, so refresh a private copy of the read-only host mount.
set -euo pipefail

if [[ -d "${HOME}/.ssh-host" ]]; then
  rm -rf "${HOME}/.ssh"
  cp -a "${HOME}/.ssh-host" "${HOME}/.ssh"
  chmod 700 "${HOME}/.ssh"
fi
