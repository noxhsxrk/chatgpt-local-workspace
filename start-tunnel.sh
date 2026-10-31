#!/bin/bash
set -euo pipefail

cd "$(dirname "$0")"
PROJECT_DIR="$(pwd -P)"

if [[ "$(uname -s)" != Darwin ]]; then
  echo "This launcher currently supports macOS only." >&2
  exit 1
fi
if ! command -v brew >/dev/null 2>&1; then
  echo "Homebrew is required. Install it from https://brew.sh/ and run this script again." >&2
  exit 1
fi

if [[ -z "${WORKSPACE_MCP_BINARY:-}" ]] && ! command -v go >/dev/null 2>&1; then
  echo "Installing Go with Homebrew..."
  brew install go
fi
if ! command -v tunnel-client >/dev/null 2>&1; then
  echo "Installing the official OpenAI tunnel-client with Homebrew..."
  brew install openai/tools/tunnel-client
fi

DEFAULT_WORKSPACE="$HOME/mcp-demo-workspace"
read -r -p "Workspace directory [$DEFAULT_WORKSPACE]: " workspace
workspace="${workspace:-$DEFAULT_WORKSPACE}"
# Support the common ~/ form without evaluating arbitrary shell input.
if [[ "$workspace" == '~/'* ]]; then
  workspace="$HOME/${workspace:2}"
fi
mkdir -p "$workspace"
export WORKSPACE_ROOT="$(cd "$workspace" && pwd -P)"

read -r -p "Tunnel ID (tunnel_...): " tunnel_id
if [[ ! "$tunnel_id" =~ ^tunnel_[a-zA-Z0-9_-]+$ ]]; then
  echo "Invalid Tunnel ID." >&2
  exit 1
fi
printf 'Runtime API key (input hidden): '
read -r -s CONTROL_PLANE_API_KEY
printf '\n'
if [[ -z "$CONTROL_PLANE_API_KEY" ]]; then
  echo "A runtime API key is required." >&2
  exit 1
fi
export CONTROL_PLANE_API_KEY

if [[ -n "${WORKSPACE_MCP_BINARY:-}" ]]; then
  server_binary="$WORKSPACE_MCP_BINARY"
else
  echo "Running tests..."
  go test ./...
  echo "Building MCP server..."
  server_binary="$PROJECT_DIR/own-mcp-workspace"
  go build -o "$server_binary" .
fi

# A fresh profile avoids silently replacing an existing user configuration.
profile="own-workspace-$(date +%Y%m%d%H%M%S)-$$"
echo "Creating tunnel profile: $profile"
tunnel-client init \
  --sample sample_mcp_stdio_local \
  --profile "$profile" \
  --tunnel-id "$tunnel_id" \
  --mcp-command "$server_binary"

echo "Checking tunnel configuration..."
tunnel-client doctor --profile "$profile" --explain
echo "Starting tunnel for $WORKSPACE_ROOT. Press Ctrl+C to stop."
exec tunnel-client run --profile "$profile"
