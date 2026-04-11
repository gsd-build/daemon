#!/usr/bin/env bash
#
# Stress test: Push claude -p --resume toward context limits to see if compaction fires.
#
# Strategy: Each turn asks for ~2k tokens of new content + verbatim recall of all prior
# content, so context roughly doubles early then grows linearly. Should hit 100k+ fast.
#
# The REAL compaction signal is: total_context (cache_create + cache_read + input) drops
# between turns. Cache expiry (5min TTL) reshuffles tokens between buckets but doesn't
# change total — we track total to avoid false positives.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
LOG_DIR="$SCRIPT_DIR/stress-output"
rm -rf "$LOG_DIR"
mkdir -p "$LOG_DIR"

TURNS=${1:-40}
SESSION_ID=""
PREV_TOTAL=0

echo "=== Compaction Stress Test ==="
echo "Turns: $TURNS"
echo ""

# --- Turn 0: Seed with a big initial payload ---
echo "[turn 0] Starting session with large seed..."

TURN0_OUT="$LOG_DIR/turn_000.jsonl"
claude -p \
  --output-format stream-json \
  --verbose \
  --model claude-haiku-4-5-20251001 \
  "You are in a context stress test. Write a 2000-word essay about the history of computing, from Babbage to modern GPUs. Include specific dates, names, and technical details. End with the exact word count." \
  > "$TURN0_OUT" 2>"$LOG_DIR/turn_000.stderr" || true

SESSION_ID=$(jq -r 'select(.type == "result") | .session_id // empty' "$TURN0_OUT" | head -1)
if [[ -z "$SESSION_ID" ]]; then
  echo "FATAL: No session_id. Output:"
  cat "$TURN0_OUT"
  exit 1
fi

extract_tokens() {
  local file="$1"
  local input cache_create cache_read output total
  input=$(jq -r 'select(.type == "result") | .usage.input_tokens // 0' "$file" | head -1)
  cache_create=$(jq -r 'select(.type == "result") | .usage.cache_creation_input_tokens // 0' "$file" | head -1)
  cache_read=$(jq -r 'select(.type == "result") | .usage.cache_read_input_tokens // 0' "$file" | head -1)
  output=$(jq -r 'select(.type == "result") | .usage.output_tokens // 0' "$file" | head -1)
  total=$(( input + cache_create + cache_read ))
  echo "$input $cache_create $cache_read $output $total"
}

read INPUT CACHE_CREATE CACHE_READ OUTPUT TOTAL <<< "$(extract_tokens "$TURN0_OUT")"
echo "[turn 0] session=$SESSION_ID total_ctx=$TOTAL (in=$INPUT cc=$CACHE_CREATE cr=$CACHE_READ) out=$OUTPUT"
echo "turn,total_context,input,cache_create,cache_read,output,delta" > "$LOG_DIR/token_log.csv"
echo "0,$TOTAL,$INPUT,$CACHE_CREATE,$CACHE_READ,$OUTPUT,0" >> "$LOG_DIR/token_log.csv"
PREV_TOTAL=$TOTAL

# --- Growth turns ---
TOPICS=("quantum entanglement and Bell's theorem" \
        "deep sea hydrothermal vents and chemosynthesis" \
        "Gothic cathedral construction techniques" \
        "bebop jazz harmony and chord substitutions" \
        "volcanic hotspot theory and mantle plumes" \
        "elliptic curve cryptography" \
        "Lagrange points and orbital mechanics" \
        "sourdough fermentation microbiology" \
        "Ramsey theory and combinatorial bounds" \
        "Caravaggio's use of chiaroscuro" \
        "LLVM compiler pipeline architecture" \
        "tidal resonance in the Bay of Fundy" \
        "Nash equilibrium in repeated games" \
        "mycelial networks in forest ecosystems" \
        "Nyquist-Shannon sampling theorem" \
        "Roman concrete and Pantheon engineering" \
        "turbulent flow and Reynolds numbers" \
        "Bauhaus typography principles" \
        "prospect theory and loss aversion" \
        "CNO cycle in stellar nucleosynthesis")

COMPACTION_DETECTED=false
COMPACTION_TURN=0

for ((i=1; i<=TURNS; i++)); do
  TURN_PAD=$(printf "%03d" "$i")
  TOPIC="${TOPICS[$(( (i - 1) % ${#TOPICS[@]} ))]}"
  TURN_OUT="$LOG_DIR/turn_${TURN_PAD}.jsonl"

  # Aggressive context growth prompt
  PROMPT="Turn $i. Write 10 detailed paragraphs about: $TOPIC. Include specific equations, dates, names, and technical terminology. Then reproduce a COMPLETE numbered list of every topic covered so far in this session with a 2-sentence summary of each. Do not abbreviate or skip any prior topics."

  echo -n "[turn $i] topic='$TOPIC'... "

  claude -p \
    --output-format stream-json \
    --verbose \
    --model claude-haiku-4-5-20251001 \
    --resume "$SESSION_ID" \
    "$PROMPT" \
    > "$TURN_OUT" 2>"$LOG_DIR/turn_${TURN_PAD}.stderr" || true

  # Check for system events about compaction
  if jq -e 'select(.type == "system") | .message // .subtype // empty' "$TURN_OUT" 2>/dev/null \
     | grep -qi "compact\|summar\|truncat\|compress\|context.*reduc"; then
    echo ""
    echo "  *** COMPACTION SYSTEM EVENT ***"
    jq -r 'select(.type == "system")' "$TURN_OUT"
    COMPACTION_DETECTED=true
    COMPACTION_TURN=$i
  fi

  read INPUT CACHE_CREATE CACHE_READ OUTPUT TOTAL <<< "$(extract_tokens "$TURN_OUT")"

  # Check error
  ERROR=$(jq -r 'select(.type == "result") | .is_error // false' "$TURN_OUT" | head -1)
  if [[ "$ERROR" == "true" ]]; then
    ERROR_MSG=$(jq -r 'select(.type == "result") | .result // "unknown"' "$TURN_OUT" | head -1)
    echo "ERROR: $ERROR_MSG"
    echo "$i,$TOTAL,$INPUT,$CACHE_CREATE,$CACHE_READ,$OUTPUT,ERROR" >> "$LOG_DIR/token_log.csv"
    break
  fi

  # Delta on TOTAL context (immune to cache bucket reshuffling)
  DELTA=0
  if [[ "$PREV_TOTAL" -gt 0 && "$TOTAL" -gt 0 ]]; then
    DELTA=$(( TOTAL - PREV_TOTAL ))
  fi

  MARKER=""
  if [[ "$DELTA" -lt -1000 ]]; then
    MARKER=" *** COMPACTION: total dropped by $(( -DELTA )) tokens"
    COMPACTION_DETECTED=true
    COMPACTION_TURN=$i
  fi

  echo "total=$TOTAL (in=$INPUT cc=$CACHE_CREATE cr=$CACHE_READ) out=$OUTPUT delta=$DELTA$MARKER"
  echo "$i,$TOTAL,$INPUT,$CACHE_CREATE,$CACHE_READ,$OUTPUT,$DELTA" >> "$LOG_DIR/token_log.csv"
  PREV_TOTAL=$TOTAL

  # Haiku has 200k context — push past it to see what happens
  if [[ "$TOTAL" -gt 250000 ]]; then
    echo ""
    echo "Hit 250k total context (past Haiku 200k limit). Stopping."
    break
  fi
done

echo ""
echo "=== RESULTS ==="
echo "Session: $SESSION_ID"
echo "Compaction detected: $COMPACTION_DETECTED"
if [[ "$COMPACTION_DETECTED" == "true" ]]; then
  echo "First compaction at turn: $COMPACTION_TURN"
fi
echo ""
echo "Context growth:"
column -t -s',' "$LOG_DIR/token_log.csv"
echo ""
echo "Raw data: $LOG_DIR/"
