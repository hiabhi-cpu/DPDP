#!/usr/bin/env bash
# =============================================================================
# DPDP Consent Manager — Start all Phase 1 services
# Usage: ./scripts/start-dev.sh
# Stop:  ./scripts/stop-dev.sh
# =============================================================================

set -e
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LOG_DIR="$REPO_ROOT/.service-logs"
PID_FILE="$REPO_ROOT/.service-pids"

mkdir -p "$LOG_DIR"
rm -f "$PID_FILE"

# Load environment
set -a
source "$REPO_ROOT/.env"
set +a

echo "🚀 Starting DPDP Consent Manager services..."

start_service() {
  local svc=$1
  local port=$2
  local extra_env=$3

  echo "  ▶ Starting $svc on :$port..."
  eval "env $extra_env go run $REPO_ROOT/services/$svc/. > $LOG_DIR/$svc.log 2>&1" &
  local pid=$!
  echo "$svc:$pid" >> "$PID_FILE"
  echo "    PID=$pid → logs: $LOG_DIR/$svc.log"
}

# Kill anything on our ports first (no sudo required — kill our own processes)
for port in 9000 9001 9004 9006; do
  pid=$(lsof -ti tcp:$port 2>/dev/null) && kill "$pid" 2>/dev/null || true
done
sleep 1

# Start services in dependency order
start_service "audit-service"        "9001" ""
sleep 2

start_service "auth-service"         "9006" \
  "JWT_PRIVATE_KEY_PATH=$REPO_ROOT/services/auth-service/keys/auth_private.pem JWT_PUBLIC_KEY_PATH=$REPO_ROOT/services/auth-service/keys/auth_public.pem"
sleep 2

start_service "notification-service" "9004" \
  "LOCAL_SECRETS_PATH=$REPO_ROOT/secrets/local_hospital_keys.json"
sleep 2

start_service "consent-service"      "9000" \
  "LOCAL_SECRETS_PATH=$REPO_ROOT/secrets/local_hospital_keys.json"

echo ""
echo "⏳ Waiting for services to initialize (10s)..."
sleep 10

echo ""
echo "=== Health Checks ==="
for svc_port in "auth-service:9006" "audit-service:9001" "notification-service:9004" "consent-service:9000"; do
  svc="${svc_port%%:*}"
  port="${svc_port##*:}"
  if curl -sf "http://localhost:$port/health" > /dev/null 2>&1; then
    echo "  ✅ $svc → http://localhost:$port"
  else
    echo "  ❌ $svc FAILED — last 5 lines of log:"
    tail -5 "$LOG_DIR/$svc.log" | sed 's/^/    /'
  fi
done

echo ""
echo "📋 PIDs saved to .service-pids"
echo "   Stop with: ./scripts/stop-dev.sh"
echo "   Logs in:   .service-logs/"
