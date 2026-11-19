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

source_commit=ddef4ef30d54c5c9bc376f449724d82e6831029f
archive_hash=68b901291090cb8b8e6b58df8b8fc38f84782e9eb828fc7ebe88fdef72a6be12
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
unset GH_TOKEN GITHUB_TOKEN
echo "$archive_hash  $archive" | sha256sum --check --status
tar -xzf "$archive" -C "$scratch/old" manifest.json release-set.json tend tend-server tend-runner
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

# Only a disposable hosted VM provisions a fresh service account. Its manager,
# processes, sockets and generated credentials belong exclusively to this test.
scratch=$(realpath "$scratch")
head=$(git rev-parse HEAD)
go run ./internal/release/packagecmd -arch amd64 -commit "$head" -output "$scratch/packages"
mkdir "$scratch/bundle"
tar -xzf "$scratch/packages/tend-main-$head-linux-amd64.tar.gz" -C "$scratch/bundle"
cp "$fixture/"{cross_version,model,controlled}.py "$scratch/"
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
