#!/usr/bin/env bash
# =============================================================================
# DPDP Consent Manager — Stop all Phase 1 services
# Usage: ./scripts/stop-dev.sh
# =============================================================================

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PID_FILE="$REPO_ROOT/.service-pids"

echo "🛑 Stopping DPDP Consent Manager services..."

if [ -f "$PID_FILE" ]; then
  while IFS=: read -r svc pid; do
    if kill -0 "$pid" 2>/dev/null; then
      kill "$pid" && echo "  ✅ Stopped $svc (PID $pid)"
    else
      echo "  ⚠️  $svc (PID $pid) already stopped"
    fi
  done < "$PID_FILE"
  rm -f "$PID_FILE"
fi

# Belt-and-suspenders: kill anything on service ports
for port in 9000 9001 9004 9006; do
  pid=$(lsof -ti tcp:$port 2>/dev/null) && kill "$pid" 2>/dev/null && echo "  🔪 Killed port $port" || true
done

echo "✅ All services stopped"
