#!/usr/bin/env bash
#
# Spike: Does `claude -p --resume` trigger context compaction?
#
# Approach:
#   1. Start a fresh session
#   2. Loop N times, resuming the same session with prompts that force context growth
#   3. Log input_tokens from each result event to track whether compaction fires
#
# Signal:
#   - Compaction: input_tokens drops between consecutive turns
#   - No compaction: input_tokens grows monotonically until error
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
LOG_DIR="$SCRIPT_DIR/output"
rm -rf "$LOG_DIR"
mkdir -p "$LOG_DIR"

TURNS=${1:-30}  # number of resume turns; override with ./run.sh 50
SESSION_ID=""
PREV_INPUT_TOKENS=0

echo "=== Compaction Spike ==="
echo "Turns: $TURNS"
echo "Log dir: $LOG_DIR"
echo ""

# --- Turn 0: Start fresh session ---
echo "[turn 0] Starting fresh session..."

TURN0_OUT="$LOG_DIR/turn_000.jsonl"
claude -p \
  --output-format stream-json \
  --verbose \
  "You are participating in a context compaction test. Respond with exactly: SESSION_STARTED" \
  > "$TURN0_OUT" 2>"$LOG_DIR/turn_000.stderr" || true

# Extract session_id from the result event
SESSION_ID=$(jq -r 'select(.type == "result") | .session_id // empty' "$TURN0_OUT" | head -1)
if [[ -z "$SESSION_ID" ]]; then
  echo "FATAL: No session_id in turn 0 output. Raw output:"
  cat "$TURN0_OUT"
  exit 1
fi

# Extract token counts — usage is at top level of result event
INPUT_TOKENS=$(jq -r 'select(.type == "result") | (.usage.input_tokens // 0) + (.usage.cache_read_input_tokens // 0)' "$TURN0_OUT" | head -1)
CACHE_CREATE=$(jq -r 'select(.type == "result") | .usage.cache_creation_input_tokens // 0' "$TURN0_OUT" | head -1)
CACHE_READ=$(jq -r 'select(.type == "result") | .usage.cache_read_input_tokens // 0' "$TURN0_OUT" | head -1)
OUTPUT_TOKENS=$(jq -r 'select(.type == "result") | .usage.output_tokens // 0' "$TURN0_OUT" | head -1)

echo "[turn 0] session_id=$SESSION_ID input=$INPUT_TOKENS cache_create=$CACHE_CREATE cache_read=$CACHE_READ output=$OUTPUT_TOKENS"
echo "turn,input_tokens,cache_create,cache_read,output_tokens" > "$LOG_DIR/token_log.csv"
echo "0,$INPUT_TOKENS,$CACHE_CREATE,$CACHE_READ,$OUTPUT_TOKENS" >> "$LOG_DIR/token_log.csv"
PREV_INPUT_TOKENS=$INPUT_TOKENS

# --- Turns 1..N: Resume with context-growing prompts ---
TOPICS=("quantum computing" "marine biology" "medieval architecture" "jazz theory" \
        "volcanic geology" "cryptography" "orbital mechanics" "fermentation science" \
        "graph theory" "Renaissance painting" "compiler design" "tidal patterns" \
        "game theory" "mycology" "signal processing" "ancient Rome" \
        "fluid dynamics" "typography" "behavioral economics" "stellar nucleosynthesis" \
        "neurolinguistics" "category theory" "permaculture" "tensor calculus" \
        "epidemiology" "origami mathematics" "phonology" "plate tectonics" \
        "stochastic processes" "lichens")

COMPACTION_DETECTED=false

for ((i=1; i<=TURNS; i++)); do
  TURN_PAD=$(printf "%03d" "$i")
  TOPIC="${TOPICS[$(( (i - 1) % ${#TOPICS[@]} ))]}"
  TURN_OUT="$LOG_DIR/turn_${TURN_PAD}.jsonl"

  # Prompt designed to grow context: asks Claude to recall + add new content
  PROMPT="Turn $i of compaction test. Write 3 detailed paragraphs about $TOPIC. Then list every topic you have written about so far in this session, with a one-sentence summary of each."

  echo -n "[turn $i] Resuming with topic='$TOPIC'... "

  claude -p \
    --output-format stream-json \
    --verbose \
    --resume "$SESSION_ID" \
    "$PROMPT" \
    > "$TURN_OUT" 2>"$LOG_DIR/turn_${TURN_PAD}.stderr" || true

  # Check for any system events mentioning compaction
  SYSTEM_EVENTS=$(jq -r 'select(.type == "system") | .message // .subtype // empty' "$TURN_OUT" 2>/dev/null || true)
  if echo "$SYSTEM_EVENTS" | grep -qi "compact\|summar\|truncat\|compress"; then
    echo ""
    echo "  *** COMPACTION SYSTEM EVENT DETECTED ***"
    echo "  $SYSTEM_EVENTS"
    COMPACTION_DETECTED=true
  fi

  # Extract token counts — usage is at top level of result event
  INPUT_TOKENS=$(jq -r 'select(.type == "result") | (.usage.input_tokens // 0) + (.usage.cache_read_input_tokens // 0)' "$TURN_OUT" | head -1)
  CACHE_CREATE=$(jq -r 'select(.type == "result") | .usage.cache_creation_input_tokens // 0' "$TURN_OUT" | head -1)
  CACHE_READ=$(jq -r 'select(.type == "result") | .usage.cache_read_input_tokens // 0' "$TURN_OUT" | head -1)
  OUTPUT_TOKENS=$(jq -r 'select(.type == "result") | .usage.output_tokens // 0' "$TURN_OUT" | head -1)

  # Check for error
  ERROR=$(jq -r 'select(.type == "result") | .is_error // false' "$TURN_OUT" | head -1)
  if [[ "$ERROR" == "true" ]]; then
    ERROR_MSG=$(jq -r 'select(.type == "result") | .result // "unknown"' "$TURN_OUT" | head -1)
    echo "ERROR at turn $i: $ERROR_MSG"
    echo "$i,$INPUT_TOKENS,$CACHE_CREATE,$CACHE_READ,$OUTPUT_TOKENS,ERROR" >> "$LOG_DIR/token_log.csv"
    break
  fi

  # Detect compaction via token drop (input_tokens = uncached + cache_read)
  TOTAL_INPUT=$((INPUT_TOKENS))
  DELTA=""
  if [[ "$TOTAL_INPUT" -gt 0 && "$PREV_INPUT_TOKENS" -gt 0 ]]; then
    if [[ "$TOTAL_INPUT" -lt "$PREV_INPUT_TOKENS" ]]; then
      DROP=$(( PREV_INPUT_TOKENS - TOTAL_INPUT ))
      DELTA=" *** DROPPED by $DROP tokens (compaction?)"
      COMPACTION_DETECTED=true
    else
      GROWTH=$(( TOTAL_INPUT - PREV_INPUT_TOKENS ))
      DELTA=" (+$GROWTH)"
    fi
  fi

  echo "input=$INPUT_TOKENS cache_create=$CACHE_CREATE cache_read=$CACHE_READ output=$OUTPUT_TOKENS$DELTA"
  echo "$i,$INPUT_TOKENS,$CACHE_CREATE,$CACHE_READ,$OUTPUT_TOKENS" >> "$LOG_DIR/token_log.csv"
  PREV_INPUT_TOKENS=$TOTAL_INPUT

  # Early exit if we're way past 200k context (1M model has room but let's not waste credits)
  if [[ "$INPUT_TOKENS" -gt 200000 ]]; then
    echo ""
    echo "Hit 200k input tokens without compaction. Stopping."
    break
  fi
done

echo ""
echo "=== Results ==="
echo "Session ID: $SESSION_ID"
echo "Compaction detected: $COMPACTION_DETECTED"
echo ""
echo "Token growth curve:"
echo "turn,input_tokens,cache_create,cache_read,output_tokens"
cat "$LOG_DIR/token_log.csv"
echo ""
echo "Raw output in: $LOG_DIR/"
