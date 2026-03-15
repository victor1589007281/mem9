#!/usr/bin/env bash
set -euo pipefail

# Setup the Vmem Memory Agent in OpenClaw.
#
# This script:
#   1. Creates the vmem-memory agent via `openclaw agents add`
#   2. Copies bootstrap files (SOUL.md, IDENTITY.md, etc.) to the agent's workspace
#   3. Prints the config snippet to add to openclaw.json
#
# Usage:
#   bash setup-agent.sh
#   bash setup-agent.sh --model bailian/qwen3-coder-plus
#
# Prerequisites:
#   - OpenClaw installed and configured
#   - Coding Plan models configured (see https://help.aliyun.com/zh/model-studio/openclaw-coding-plan)

AGENT_ID="vmem-memory"
AGENT_NAME="Vmem Memory"
WORKSPACE="${HOME}/.openclaw/workspace-${AGENT_ID}"
MODEL="${1:-bailian/qwen3-coder-plus}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BOOTSTRAP_DIR="${SCRIPT_DIR}/../agents/memory"

echo "=== Vmem Memory Agent Setup ==="
echo ""

# Step 1: Create agent
if openclaw agents list --json 2>/dev/null | grep -q "\"${AGENT_ID}\""; then
  echo "[✓] Agent '${AGENT_ID}' already exists"
else
  echo "[+] Creating agent '${AGENT_ID}'..."
  openclaw agents add "${AGENT_NAME}" \
    --workspace "${WORKSPACE}" \
    --model "${MODEL}" \
    --non-interactive \
    --json 2>/dev/null || {
    echo "[!] openclaw agents add failed, creating workspace manually..."
    mkdir -p "${WORKSPACE}"
  }
  echo "[✓] Agent created"
fi

# Step 2: Ensure workspace exists and copy bootstrap files
mkdir -p "${WORKSPACE}"

BOOTSTRAP_FILES=(SOUL.md IDENTITY.md AGENTS.md TOOLS.md USER.md)
for f in "${BOOTSTRAP_FILES[@]}"; do
  src="${BOOTSTRAP_DIR}/${f}"
  dst="${WORKSPACE}/${f}"
  if [ -f "${src}" ]; then
    cp "${src}" "${dst}"
    echo "[✓] Copied ${f}"
  else
    echo "[!] Missing ${src}, skipping"
  fi
done

echo ""
echo "=== Agent workspace ready at ${WORKSPACE} ==="
echo ""
echo "Add the following to your ~/.openclaw/openclaw.json:"
echo ""
cat <<'JSONEOF'
{
  "agents": {
    "list": [
      {
        "id": "vmem-memory",
        "name": "Vmem Memory",
        "workspace": "~/.openclaw/workspace-vmem-memory",
        "model": {
          "primary": "bailian/qwen3-coder-plus",
          "fallbacks": [
            "bailian/glm-4.7",
            "bailian/MiniMax-M2.5",
            "bailian/qwen3.5-plus"
          ]
        },
        "subagents": { "allowAgents": ["*"] }
      }
    ]
  }
}
JSONEOF
echo ""
echo "Then restart: openclaw gateway restart"
echo ""
echo "=== Done ==="
