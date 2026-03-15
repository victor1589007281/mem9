#!/usr/bin/env bash
set -euo pipefail

# Idempotent Ollama + bge-m3 deployment script for mnemo-server embedding.
# Safe to run multiple times — each step checks before acting.
#
# Usage:
#   ./scripts/setup-ollama-bge-m3.sh           # Install + pull + verify
#   ./scripts/setup-ollama-bge-m3.sh --status   # Check status only
#
# After running, configure mnemo-server:
#   MNEMO_EMBED_BASE_URL=http://localhost:11434/v1
#   MNEMO_EMBED_MODEL=bge-m3
#   MNEMO_EMBED_DIMS=1024
#   MNEMO_EMBED_API_KEY=ollama   (any non-empty string)

readonly MODEL="bge-m3"
readonly EMBED_DIMS=1024
readonly OLLAMA_HOST="${OLLAMA_HOST:-http://localhost:11434}"
readonly HEALTH_RETRIES=30
readonly HEALTH_INTERVAL=2

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

info()  { printf "${BLUE}[INFO]${NC}  %s\n" "$*"; }
ok()    { printf "${GREEN}[OK]${NC}    %s\n" "$*"; }
warn()  { printf "${YELLOW}[WARN]${NC}  %s\n" "$*"; }
fail()  { printf "${RED}[FAIL]${NC}  %s\n" "$*"; exit 1; }

detect_os() {
  case "$(uname -s)" in
    Darwin) echo "macos" ;;
    Linux)  echo "linux" ;;
    *)      fail "Unsupported OS: $(uname -s). Only macOS and Linux are supported." ;;
  esac
}

is_ollama_installed() {
  command -v ollama &>/dev/null
}

is_ollama_running() {
  curl -sf "${OLLAMA_HOST}/api/tags" &>/dev/null
}

is_model_available() {
  local tags
  tags=$(curl -sf "${OLLAMA_HOST}/api/tags" 2>/dev/null || echo "")
  echo "$tags" | grep -q "\"${MODEL}\""
}

# ---------------------------------------------------------------------------
# Step 1: Install Ollama
# ---------------------------------------------------------------------------
install_ollama() {
  if is_ollama_installed; then
    local ver
    ver=$(ollama --version 2>/dev/null || echo "unknown")
    ok "Ollama already installed (${ver})"
    return 0
  fi

  local os
  os=$(detect_os)
  info "Installing Ollama on ${os}..."

  case "$os" in
    macos)
      if command -v brew &>/dev/null; then
        info "Installing via Homebrew..."
        brew install ollama
      else
        info "Homebrew not found, installing via official script..."
        curl -fsSL https://ollama.com/install.sh | sh
      fi
      ;;
    linux)
      info "Installing via official script..."
      curl -fsSL https://ollama.com/install.sh | sh
      ;;
  esac

  if is_ollama_installed; then
    ok "Ollama installed successfully"
  else
    fail "Ollama installation failed"
  fi
}

# ---------------------------------------------------------------------------
# Step 2: Start Ollama service
# ---------------------------------------------------------------------------
start_ollama() {
  if is_ollama_running; then
    ok "Ollama service already running at ${OLLAMA_HOST}"
    return 0
  fi

  info "Starting Ollama service..."
  local os
  os=$(detect_os)

  case "$os" in
    macos)
      # macOS: Ollama.app or `ollama serve` in background
      if [ -d "/Applications/Ollama.app" ]; then
        open -a Ollama
        info "Launched Ollama.app, waiting for service..."
      else
        nohup ollama serve > /tmp/ollama.log 2>&1 &
        info "Started ollama serve (PID: $!), logs at /tmp/ollama.log"
      fi
      ;;
    linux)
      # Linux: try systemd first, then manual
      if command -v systemctl &>/dev/null && systemctl list-unit-files ollama.service &>/dev/null; then
        sudo systemctl start ollama 2>/dev/null || true
        info "Started via systemctl"
      else
        nohup ollama serve > /tmp/ollama.log 2>&1 &
        info "Started ollama serve (PID: $!), logs at /tmp/ollama.log"
      fi
      ;;
  esac

  # Wait for healthy
  info "Waiting for Ollama to become healthy..."
  for i in $(seq 1 $HEALTH_RETRIES); do
    if is_ollama_running; then
      ok "Ollama service is healthy (after ${i}s)"
      return 0
    fi
    sleep "$HEALTH_INTERVAL"
  done

  fail "Ollama service did not become healthy within $((HEALTH_RETRIES * HEALTH_INTERVAL))s"
}

# ---------------------------------------------------------------------------
# Step 3: Pull bge-m3 model
# ---------------------------------------------------------------------------
pull_model() {
  if is_model_available; then
    ok "Model '${MODEL}' already available"
    return 0
  fi

  info "Pulling model '${MODEL}' (this may take a few minutes on first run)..."
  ollama pull "$MODEL"

  if is_model_available; then
    ok "Model '${MODEL}' pulled successfully"
  else
    fail "Model '${MODEL}' pull failed"
  fi
}

# ---------------------------------------------------------------------------
# Step 4: Verify embedding works
# ---------------------------------------------------------------------------
verify_embedding() {
  info "Verifying embedding with '${MODEL}'..."

  local response
  response=$(curl -sf "${OLLAMA_HOST}/api/embed" \
    -d "{\"model\": \"${MODEL}\", \"input\": \"hello world\"}" 2>/dev/null || echo "")

  if [ -z "$response" ]; then
    fail "Embedding request failed — Ollama may not be responding"
  fi

  local has_embeddings
  has_embeddings=$(echo "$response" | python3 -c "
import sys, json
data = json.load(sys.stdin)
embs = data.get('embeddings', [])
if embs and len(embs) > 0:
    vec = embs[0]
    print(f'dims={len(vec)}')
else:
    print('none')
" 2>/dev/null || echo "error")

  if [[ "$has_embeddings" == none ]] || [[ "$has_embeddings" == error ]]; then
    fail "Embedding verification failed — model returned no vectors"
  fi

  local actual_dims
  actual_dims=$(echo "$has_embeddings" | sed 's/dims=//')
  ok "Embedding verified: ${has_embeddings}"

  if [ "$actual_dims" != "$EMBED_DIMS" ]; then
    warn "Dimension mismatch: expected ${EMBED_DIMS}, got ${actual_dims}"
    warn "Update MNEMO_EMBED_DIMS=${actual_dims} in your server config"
  fi
}

# ---------------------------------------------------------------------------
# Step 5: Print server config
# ---------------------------------------------------------------------------
print_config() {
  echo ""
  echo "═══════════════════════════════════════════════════════════════"
  echo "  Ollama + bge-m3 is ready for mnemo-server"
  echo "═══════════════════════════════════════════════════════════════"
  echo ""
  echo "  Add these environment variables to mnemo-server:"
  echo ""
  echo "    export MNEMO_EMBED_BASE_URL=${OLLAMA_HOST}/v1"
  echo "    export MNEMO_EMBED_MODEL=${MODEL}"
  echo "    export MNEMO_EMBED_DIMS=${EMBED_DIMS}"
  echo "    export MNEMO_EMBED_API_KEY=ollama"
  echo ""
  echo "  Or in .env file:"
  echo ""
  echo "    MNEMO_EMBED_BASE_URL=${OLLAMA_HOST}/v1"
  echo "    MNEMO_EMBED_MODEL=${MODEL}"
  echo "    MNEMO_EMBED_DIMS=${EMBED_DIMS}"
  echo "    MNEMO_EMBED_API_KEY=ollama"
  echo ""
  echo "═══════════════════════════════════════════════════════════════"
}

# ---------------------------------------------------------------------------
# --status: quick health check
# ---------------------------------------------------------------------------
status_check() {
  echo "mnemo Ollama + bge-m3 Status"
  echo "────────────────────────────"

  if is_ollama_installed; then
    ok "Ollama binary: $(command -v ollama)"
  else
    fail "Ollama: not installed"
  fi

  if is_ollama_running; then
    ok "Ollama service: running at ${OLLAMA_HOST}"
  else
    warn "Ollama service: not running"
  fi

  if is_ollama_running && is_model_available; then
    ok "Model '${MODEL}': available"
  else
    warn "Model '${MODEL}': not pulled or service not running"
  fi

  if is_ollama_running && is_model_available; then
    verify_embedding
  fi
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
main() {
  if [[ "${1:-}" == "--status" ]]; then
    status_check
    exit 0
  fi

  echo "╔═══════════════════════════════════════════╗"
  echo "║  mnemo: Ollama + bge-m3 Setup             ║"
  echo "╚═══════════════════════════════════════════╝"
  echo ""

  install_ollama
  start_ollama
  pull_model
  verify_embedding
  print_config
}

main "$@"
