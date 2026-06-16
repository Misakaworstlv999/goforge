#!/bin/bash
# Remind to update artifacts before ending session.
PROJECT_DIR="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
cd "$PROJECT_DIR" 2>/dev/null || exit 0

DIRTY=$(git status --porcelain 2>/dev/null)

echo "=== End-of-Session Checklist ==="
if [ -n "$DIRTY" ]; then
    echo "WARNING: Uncommitted changes detected:"
    echo "$DIRTY" | sed 's/^/  /'
    echo ""
fi
echo "Before leaving:"
echo "  1. Update progress.md with current state"
echo "  2. Update feature_list.json with feature status"
echo "  3. Run ./init.sh to verify clean state"
echo "  4. git commit if work is in a safe checkpoint"
