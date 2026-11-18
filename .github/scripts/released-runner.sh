#!/usr/bin/env bash
# Exercise an immutable release against the checked-out candidate. Run from
# the repository root on linux/amd64. A local archive can be used offline.
set -euo pipefail

source_commit=ddef4ef30d54c5c9bc376f449724d82e6831029f
archive_hash=68b901291090cb8b8e6b58df8b8fc38f84782e9eb828fc7ebe88fdef72a6be12
release=main-$source_commit
asset=tend-$release-linux-amd64.tar.gz
fixture=internal/e2e/testdata/rolling
scratch=$(mktemp -d)
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
python3 "$fixture/cross_version.py" --historical-dir "$scratch/old" --source "$source_commit" \
  --protocol "$minimum" --candidate-server "$scratch/candidate-server" --seed "$scratch/seed"
