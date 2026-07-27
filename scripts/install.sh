#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
install_dir="${HOME}/.local/bin"
mkdir -p "$install_dir"

go build -o "$install_dir/gpt-github-gateway" "$repo_root/cmd/gpt-github-gateway"
printf 'installed %s\n' "$install_dir/gpt-github-gateway"
