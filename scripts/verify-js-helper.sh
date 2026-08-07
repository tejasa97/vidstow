#!/bin/sh
set -eu

app_path=${1:-}
if [ -z "$app_path" ]; then
  echo "verify-js-helper: missing app or executable path" >&2
  exit 2
fi

helper_dir=$(CDPATH= cd -- "$(dirname -- "$app_path")" && pwd)

helper_name=ytdlp-js-helper
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*|Windows_NT)
    helper_name=ytdlp-js-helper.exe
    ;;
esac
case "$app_path" in
  *.exe)
    helper_name=ytdlp-js-helper.exe
    ;;
esac

helper_path="$helper_dir/$helper_name"

if [ ! -f "$helper_path" ] || [ -L "$helper_path" ]; then
  echo "verify-js-helper: missing regular sibling: $helper_path" >&2
  exit 1
fi

# Windows does not use POSIX execute bits the same way; require them elsewhere.
case "$helper_name" in
  *.exe) ;;
  *)
    if [ ! -x "$helper_path" ]; then
      echo "verify-js-helper: helper is not executable: $helper_path" >&2
      exit 1
    fi
    ;;
esac

case "$helper_path" in
  */ytdlp-js-helper|*/ytdlp-js-helper.exe) ;;
  *)
    echo "verify-js-helper: helper is not the required sibling name" >&2
    exit 1
    ;;
esac

if ! "$helper_path" --version >/dev/null 2>&1; then
  echo "verify-js-helper: helper --version failed: $helper_path" >&2
  exit 1
fi
