#!/usr/bin/env -S dumb-init bash

BUILD_DIR="$(cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd)"

function run() {
  if [[ "${DEVBOX_SHELL_ENABLED:-0}" != "1" ]]; then
    devbox run build_script "$@"
    return $?
  fi

  if [[ "$#" -lt 1 ]]; then
    echo "Usage: $0 <ONE_PASSWORD_ACCOUNT>"
  fi

  just release "$@"
}

run "$@"
