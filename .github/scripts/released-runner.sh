#!/usr/bin/env bash
# Exercise an immutable release against the checked-out candidate. Run from
# the repository root on linux/amd64. A local archive can be used offline.
set -euo pipefail

controlled=false
if [[ ${1:-} == --hosted-systemd ]]; then
  [[ ${RUNNER_ENVIRONMENT:-} == github-hosted && ${GITHUB_ACTIONS:-} == true ]]
  controlled=true
  shift
fi

source_commit=25af0d57f6e0072e7037bf577970c4bc7e052180
archive_hash=07581a965b0ecd27a07dd372ee24960880e183579b1416d23bca41ace7abcd61
release=main-$source_commit
asset=tend-$release-linux-amd64.tar.gz
fixture=internal/e2e/testdata/rolling
scratch=$(mktemp -d /tmp/tend-release-test-XXXXXXXX)
trap 'rm -rf -- "$scratch"' EXIT
mkdir "$scratch/old" "$scratch/source"
archive=${1:-$scratch/$asset}
if [[ $# == 0 ]]; then
  gh release download "$release" --repo GhostFlying/tend --pattern "$asset" --dir "$scratch"
fi
if "$controlled"; then
  dev_source=6befc2b81d5f0101abc9a9786f51ff2e9ed2ab2d
  bridge_source=38ea2dbed7f022bf3c6306798549d2cfe6ba2dfa
  fetch_bundle() {
    local commit=$1 hash=$2 dest=$3 name=tend-main-$1-linux-amd64.tar.gz
    gh release download "main-$commit" --repo GhostFlying/tend --pattern "$name" --dir "$scratch"
    echo "$hash  $scratch/$name" | sha256sum --check --status
    mkdir "$dest"
    tar -xzf "$scratch/$name" -C "$dest" manifest.json release-set.json tend tend-server tend-runner tend-updater
    jq -e --arg commit "$commit" '.format == 2 and .commit == $commit' "$dest/release-set.json" >/dev/null
  }
  fetch_bundle "$dev_source" 79f7ec826d63b375d65053243b20a68a1bfdfce4f87668a3cdd01f20d74bed52 "$scratch/dev"
  fetch_bundle "$bridge_source" 022ae971290ab24ace81d54b18fd5e596746773fd27052fd144f9cd8cf5313fc "$scratch/bridge"
fi
unset GH_TOKEN GITHUB_TOKEN
echo "$archive_hash  $archive" | sha256sum --check --status
tar -xzf "$archive" -C "$scratch/old" manifest.json release-set.json tend tend-server tend-runner tend-updater
minimum=$(jq -er '.minimum' internal/protocol/runner_compat.json)
jq -e --arg commit "$source_commit" '.format == 2 and .commit == $commit and .os == "linux" and .arch == "amd64"' "$scratch/old/release-set.json" >/dev/null
jq -e --arg release "$release" --argjson minimum "$minimum" '.release_id == $release and .protocol.max == $minimum' "$scratch/old/manifest.json" >/dev/null
git archive "$source_commit" | tar -x -C "$scratch/source"
cp "$fixture/seed.go" "$scratch/source/seed.go"
(cd "$scratch/source" && go build -o "$scratch/seed" seed.go)
go build -o "$scratch/candidate-server" ./cmd/tend-server
args=(--historical-dir "$scratch/old" --source "$source_commit" --protocol "$minimum"
      --candidate-server "$scratch/candidate-server" --seed "$scratch/seed")
python3 -B "$fixture/cross_version.py" "${args[@]}"
if ! "$controlled"; then exit; fi

mkdir "$scratch/dev-source"
git archive "$dev_source" | tar -x -C "$scratch/dev-source"
cp "$fixture/seed.go" "$scratch/dev-source/seed.go"
(cd "$scratch/dev-source" && go build -o "$scratch/dev-seed" seed.go)

# Only a disposable hosted VM provisions a fresh service account. Its manager,
# processes, sockets and generated credentials belong exclusively to this test.
scratch=$(realpath "$scratch")
head=$(git rev-parse HEAD)
go run ./internal/release/packagecmd -arch amd64 -commit "$head" -output "$scratch/packages"
mkdir "$scratch/bundle"
tar -xzf "$scratch/packages/tend-main-$head-linux-amd64.tar.gz" -C "$scratch/bundle"
# Relink the same source with a distinct Go build ID to create a third real
# runner artifact. Server, CLI, supervisor and recorded source commit are identical.
mkdir "$scratch/third"
cp -al "$scratch/bundle/." "$scratch/third/"
rm "$scratch/third/tend-runner" "$scratch/third/manifest.json" "$scratch/third/release-set.json"
CGO_ENABLED=0 go build -trimpath -buildvcs=true -ldflags=-buildid=tend-wire-third -o "$scratch/third/tend-runner" ./cmd/tend-runner
python3 - "$scratch/bundle" "$scratch/third" <<'PYTHON'
import hashlib, json, sys
from pathlib import Path
first, third = map(Path, sys.argv[1:])
for name in ("manifest.json", "release-set.json"):
    doc = json.loads((first / name).read_bytes())
    doc["release_id"] += "-wire-third"
    for artifact in doc["artifacts"]:
        if artifact["role"] == "runner":
            binary = third / artifact["path"]
            artifact.update(sha256=hashlib.file_digest(binary.open("rb"), "sha256").hexdigest(), size=binary.stat().st_size)
    if name == "release-set.json":
        doc["manifest_sha256"] = hashlib.sha256((third / "manifest.json").read_bytes()).hexdigest()
    (third / name).write_text(json.dumps(doc))
PYTHON
python3 -B "$fixture/next_window.py" "$scratch/third" "$scratch/next-window"
cp "$fixture/"{cross_version,model,controlled,generations}.py "$scratch/"
account=tend-wire-fixture
sudo useradd --create-home "$account"
cleanup() {
  sudo loginctl terminate-user "$account" || true
  sudo loginctl disable-linger "$account" || true
  sudo rm -rf -- "$scratch"
}
trap cleanup EXIT
sudo loginctl enable-linger "$account"
uid=$(id -u "$account")
sudo systemctl start "user@$uid.service"
sudo chown -R "$account:" "$scratch"
sudo -u "$account" env -i PATH=/usr/bin:/bin HOME="/home/$account" XDG_RUNTIME_DIR="/run/user/$uid" \
  DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$uid/bus" \
  python3 -B "$scratch/cross_version.py" "${args[@]}" --controlled-bundle "$scratch/bundle"
sudo -u "$account" env -i PATH=/usr/bin:/bin HOME="/home/$account" XDG_RUNTIME_DIR="/run/user/$uid" \
  DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$uid/bus" \
  python3 -B "$scratch/generations.py" "$scratch/dev" "$scratch/bundle" "$scratch/third" "$scratch/dev-seed"

sudo -u "$account" env -i PATH=/usr/bin:/bin HOME="/home/$account" XDG_RUNTIME_DIR="/run/user/$uid" \
  DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$uid/bus" \
  python3 -B "$scratch/generations.py" "$scratch/old" "$scratch/bundle" "$scratch/third" "$scratch/seed" "$scratch/next-window"

sudo -u "$account" env -i PATH=/usr/bin:/bin HOME="/home/$account" XDG_RUNTIME_DIR="/run/user/$uid" \
  DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$uid/bus" \
  python3 -B "$scratch/generations.py" "$scratch/old" "$scratch/bundle" "$scratch/third" "$scratch/seed" "$scratch/next-window" --sole


sudo -u "$account" env -i PATH=/usr/bin:/bin HOME="/home/$account" XDG_RUNTIME_DIR="/run/user/$uid" \
  DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$uid/bus" \
  python3 -B "$scratch/generations.py" "$scratch/dev" "$scratch/bridge" "$scratch/bundle" "$scratch/dev-seed" --legacy-bridge
