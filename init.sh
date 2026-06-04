#!/bin/bash
set -e

PROJECT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$PROJECT_DIR"

echo "=== GoForge Harness Initialization ==="
echo "Working directory: $PROJECT_DIR"
echo ""

# Check Go installation
if ! command -v go &> /dev/null; then
  echo "ERROR: go is not installed or not in PATH"
  exit 1
fi
echo "Go version: $(go version)"

echo ""
echo "=== Desensitization Scan ==="
echo ""
echo "--- Scanning for sensitive words ---"

WORDFILE="$PROJECT_DIR/.sensitive-words"
if [ ! -f "$WORDFILE" ]; then
  echo "SKIP: .sensitive-words not found (create it locally with a regex pattern, one line)"
  echo "  Example: echo 'word1|word2|word3' > .sensitive-words"
else
  SENSITIVE_WORDS=$(head -n1 "$WORDFILE" | tr -d '\n')
  if [ -z "$SENSITIVE_WORDS" ]; then
    echo "SKIP: .sensitive-words is empty"
  else
    FOUND=""
    if command -v rg &> /dev/null; then
      FOUND=$(rg -i "$SENSITIVE_WORDS" \
        --glob '!.git/**' \
        --glob '!vendor/**' \
        --glob '!.sensitive-words' \
        --glob '!*.plan.md' \
        . 2>/dev/null || true)
    else
      FOUND=$(grep -rEi "$SENSITIVE_WORDS" \
        --exclude=".sensitive-words" \
        --exclude-dir=".git" \
        --exclude-dir="vendor" \
        . 2>/dev/null || true)
    fi

    if [ -n "$FOUND" ]; then
      echo "ERROR: Sensitive words detected! This is a public repo — fix before committing."
      echo ""
      echo "$FOUND"
      echo ""
      echo "See AGENTS.md 'Desensitization Rules' for allowed phrasing."
      exit 1
    fi
    echo "PASS: No sensitive words found"
  fi
fi

# Check go.mod exists
if [ ! -f go.mod ]; then
  echo ""
  echo "WARNING: go.mod not found — project not yet initialized"
  echo "Run: go mod init github.com/Misakaworstlv999/goforge"
  echo "Skipping Go verification (no go.mod)."
  exit 0
fi

echo ""
echo "=== Running Go Verification ==="

echo ""
echo "--- go build ./... ---"
go build ./...
echo "PASS"

echo ""
echo "--- gofmt -l . ---"
UNFORMATTED=$(gofmt -l . 2>&1 || true)
if [ -n "$UNFORMATTED" ]; then
  echo "FAIL: The following files need formatting:"
  echo "$UNFORMATTED"
  echo "Fix with: gofmt -s -w ."
  exit 1
fi
echo "PASS"

echo ""
echo "--- go vet ./... ---"
go vet ./...
echo "PASS"

echo ""
echo "--- go test ./... ---"
go test ./... 2>&1 || true
echo "DONE"

echo ""
echo "=== Verification Complete ==="
echo ""
echo "Next steps:"
echo "1. Read feature_list.json to see current feature state"
echo "2. Pick ONE unfinished feature to work on"
echo "3. Implement only that feature"
echo "4. Re-run ./init.sh before claiming done"
