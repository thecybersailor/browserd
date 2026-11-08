#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

RUNTIME_ARTIFACT="$SCRIPT_DIR/browser_snapshot_runtime.generated.js"
if [[ -n "${BROWSER_SNAPSHOT_SOURCE:-}" ]]; then
  if [[ ! -f "$BROWSER_SNAPSHOT_SOURCE/src/capture-tree.ts" ]]; then
    echo 'BROWSER_SNAPSHOT_SOURCE must point to BotWorks packages/browser-snapshot. Example: BROWSER_SNAPSHOT_SOURCE=/absolute/path/to/botworks/packages/browser-snapshot bash internal/browser/generate_browser_snapshot_runtime.sh' >&2
    exit 2
  fi
  (
    cd "$BROWSER_SNAPSHOT_SOURCE"
    npm run build
    node dist/cli.js export-runtime > "$TMP_DIR/runtime.js"
  )
  cp "$TMP_DIR/runtime.js" "$RUNTIME_ARTIFACT"
fi
if [[ ! -s "$RUNTIME_ARTIFACT" ]]; then
  echo 'Missing exported runtime. Generate it with: BROWSER_SNAPSHOT_SOURCE=/absolute/path/to/botworks/packages/browser-snapshot bash internal/browser/generate_browser_snapshot_runtime.sh' >&2
  exit 2
fi
cp "$RUNTIME_ARTIFACT" "$TMP_DIR/runtime.js"
SOURCE_REV="BotWorks browser-snapshot runtime sha256:$(shasum -a 256 "$TMP_DIR/runtime.js" | awk '{print $1}')"

{
  printf 'package browser\n\n'
  printf '// Generated from browser-snapshot %s; DO NOT EDIT.\n' "$SOURCE_REV"
  printf 'const browserSnapshotRuntimeScript = `'
  sed 's/`/` + "`" + `/g' "$TMP_DIR/runtime.js"
  printf '`\n'
} > "$SCRIPT_DIR/browser_snapshot_runtime.go"
