#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
usage: scripts/bootstrap-local.sh \
  --gateway GATEWAY_ID \
  --project-path ABSOLUTE_PROJECT_PATH \
  --resume-session CODEX_SESSION_ID \
  [--config PATH] \
  [--force-config]
USAGE
}

gateway_id=""
project_path=""
resume_session=""
config_path="${GPT_GITHUB_GATEWAY_CONFIG:-${HOME}/.config/gpt-github-gateway/config.json}"
force_config=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --gateway) gateway_id="${2:?missing value for --gateway}"; shift 2 ;;
    --project-path) project_path="${2:?missing value for --project-path}"; shift 2 ;;
    --resume-session) resume_session="${2:?missing value for --resume-session}"; shift 2 ;;
    --config) config_path="${2:?missing value for --config}"; shift 2 ;;
    --force-config) force_config=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage; exit 2 ;;
  esac
done

[[ -n "$gateway_id" && -n "$project_path" && -n "$resume_session" ]] || { usage; exit 2; }
[[ "$project_path" = /* ]] || { echo "--project-path must be absolute" >&2; exit 2; }

for binary in go git airelay curl; do
  command -v "$binary" >/dev/null 2>&1 || { echo "missing required binary: $binary" >&2; exit 1; }
done

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bash "$repo_root/scripts/install.sh"
gateway_bin="${HOME}/.local/bin/gpt-github-gateway"

if [[ ! -f "$config_path" || "$force_config" == true ]]; then
  init_args=(
    --config "$config_path" init
    --gateway "$gateway_id"
    --bus-repository rceman/typer
    --bus-url git@github.com:rceman/typer.git
  )
  [[ "$force_config" == true ]] && init_args+=(--force)
  "$gateway_bin" "${init_args[@]}"
fi

"$gateway_bin" --config "$config_path" project add \
  --id gpt-github-gateway \
  --path "$project_path" \
  --repository rceman/gpt-github-gateway \
  --branch main \
  --airelay-profile codex \
  --session-key gpt-github-gateway_master \
  --resume-session "$resume_session"

if "$gateway_bin" --config "$config_path" status | grep -q '^running '; then
  "$gateway_bin" --config "$config_path" stop
fi
"$gateway_bin" --config "$config_path" start

status_output="$($gateway_bin --config "$config_path" status)"
printf '%s\n' "$status_output"
grep -q '^running pid=' <<<"$status_output"

doctor_output="$($gateway_bin --config "$config_path" doctor)"
printf '%s\n' "$doctor_output"
if grep -q 'failed:' <<<"$doctor_output"; then
  echo "gateway doctor reported a failure" >&2
  exit 1
fi

for _ in $(seq 1 60); do
  if curl -fsS --max-time 2 http://127.0.0.1:8787/readyz >/dev/null; then
    echo "gateway ready: http://127.0.0.1:8787/readyz"
    exit 0
  fi
  sleep 1
done

echo "gateway did not become ready" >&2
exit 1
