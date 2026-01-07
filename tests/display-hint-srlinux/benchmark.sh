#!/bin/bash
# Performance benchmark for snmp_exporter display_hint vs TC handlers
#
# Usage:
#   ./benchmark.sh run <module> [rps] [duration]   # Run benchmark
#   ./benchmark.sh compare <mod1> <mod2> [rps]     # A/B compare two modules
#   ./benchmark.sh profile <module> [duration]     # Capture CPU profile
#   ./benchmark.sh all [rps]                       # Benchmark all core modules
#
# Examples:
#   ./benchmark.sh run baseline 100 60             # 100 req/s for 60s
#   ./benchmark.sh compare baseline hint_mib 100   # A/B test
#   ./benchmark.sh profile hint_explicit 30        # 30s CPU profile
#   ./benchmark.sh all 50                          # All modules at 50 req/s

set -e
cd "$(dirname "$0")"

EXPORTER_BIN="../../snmp_exporter"
CONFIG="snmp.yml"
TARGET="${TARGET:-172.20.20.2}"
PORT="${PORT:-9116}"
EXPORTER_URL="http://localhost:$PORT"
RESULTS_DIR="./benchmark-results"

CORE_MODULES="baseline raw_octetstring hint_mib hint_explicit"

# Defaults
DEFAULT_RPS=100
DEFAULT_DURATION=60
DEFAULT_WARMUP=10

mkdir -p "$RESULTS_DIR"

log() { echo "[$(date +%H:%M:%S)] $*"; }

# Capture key metrics from /metrics endpoint
capture_metrics() {
    local output_file=$1
    curl -s "$EXPORTER_URL/metrics" | grep -E "^snmp_|^go_gc_duration|^go_memstats_alloc_bytes" > "$output_file"
}

# Extract metric value (handles both counters and gauges)
get_metric() {
    local file=$1
    local pattern=$2
    grep "$pattern" "$file" 2>/dev/null | head -1 | awk '{print $2}'
}

# Extract CPU time from pprof file
get_cpu_from_pprof() {
    local pprof_file=$1
    if [ -f "$pprof_file" ]; then
        go tool pprof -top "$pprof_file" 2>/dev/null | head -5 | grep "Total samples" | sed 's/.*Total samples = \([^(]*\)(\([^)]*\)).*/\1(\2)/'
    fi
}

# Show metrics summary for a module
show_metrics_summary() {
    local module=$1
    local metrics_file=$2

    local scrapes=$(get_metric "$metrics_file" "snmp_collection_duration_seconds_count{module=\"${module}\"}")
    local sum=$(get_metric "$metrics_file" "snmp_collection_duration_seconds_sum{module=\"${module}\"}")
    local gc_count=$(get_metric "$metrics_file" "go_gc_duration_seconds_count ")
    local gc_sum=$(get_metric "$metrics_file" "go_gc_duration_seconds_sum ")
    local alloc_total=$(get_metric "$metrics_file" "go_memstats_alloc_bytes_total ")

    if [ -n "$scrapes" ] && [ "$scrapes" != "0" ]; then
        local avg_ms=$(awk "BEGIN {printf \"%.1f\", $sum / $scrapes * 1000}" 2>/dev/null || echo "N/A")
        echo "  Scrapes: $scrapes, Avg duration: ${avg_ms}ms"
    fi
    if [ -n "$gc_count" ] && [ -n "$gc_sum" ]; then
        local gc_avg_us=$(awk "BEGIN {printf \"%.0f\", $gc_sum / $gc_count * 1000000}" 2>/dev/null || echo "N/A")
        echo "  GC cycles: $gc_count, Avg GC pause: ${gc_avg_us}us"
    fi
    if [ -n "$alloc_total" ]; then
        local alloc_mb=$(awk "BEGIN {printf \"%.0f\", $alloc_total / 1048576}" 2>/dev/null || echo "N/A")
        echo "  Total allocations: ${alloc_mb}MB"
    fi
    # Extract CPU time from corresponding pprof file
    local pprof_file="${metrics_file%.metrics}.pprof"
    local cpu_time=$(get_cpu_from_pprof "$pprof_file")
    if [ -n "$cpu_time" ]; then
        echo "  CPU time: ${cpu_time}"
    fi
}

check_exporter() {
    if ! curl -s --connect-timeout 2 "$EXPORTER_URL/metrics" >/dev/null; then
        log "Exporter not running. Starting..."
        pkill -f "snmp_exporter.*config.file=$CONFIG" 2>/dev/null || true
        sleep 0.5
        $EXPORTER_BIN --config.file="$CONFIG" --web.listen-address=":$PORT" &
        sleep 2
        if ! curl -s --connect-timeout 2 "$EXPORTER_URL/metrics" >/dev/null; then
            log "ERROR: Failed to start exporter"
            exit 1
        fi
    fi
    log "Exporter ready at $EXPORTER_URL"
}

stop_exporter() {
    pkill -9 -f "snmp_exporter.*config.file=$CONFIG" 2>/dev/null || true
    sleep 1
    # Wait for port to be released
    for i in 1 2 3 4 5; do
        if ! curl -s --connect-timeout 1 "$EXPORTER_URL/metrics" >/dev/null 2>&1; then
            return 0
        fi
        sleep 0.5
    done
}

# Force restart for fair comparison between modules
restart_exporter() {
    log "Restarting exporter (fresh process)..." >&2
    stop_exporter
    $EXPORTER_BIN --config.file="$CONFIG" --web.listen-address=":$PORT" >/dev/null 2>&1 &
    sleep 2
    if ! curl -s --connect-timeout 2 "$EXPORTER_URL/metrics" >/dev/null; then
        log "ERROR: Failed to start exporter" >&2
        exit 1
    fi
}

# Verify module works before benchmarking
verify_module() {
    local module=$1
    local result
    result=$(curl -s -w "%{http_code}" -o /dev/null "$EXPORTER_URL/snmp?module=$module&target=$TARGET")
    if [ "$result" != "200" ]; then
        log "ERROR: Module '$module' returned HTTP $result"
        return 1
    fi
}

# Calculate concurrency needed to achieve target RPS given ~100ms latency
# Formula: concurrency = rps * latency_seconds = rps * 0.1
calc_concurrency() {
    local rps=$1
    local conc=$((rps / 10))
    [ "$conc" -lt 1 ] && conc=1
    [ "$conc" -gt 200 ] && conc=200
    echo "$conc"
}

# Run warmup phase (logs to stderr)
warmup() {
    local module=$1
    local duration=${2:-$DEFAULT_WARMUP}
    local rps=${3:-$DEFAULT_RPS}
    local conc=$(calc_concurrency "$rps")

    log "Warming up: $module (~${rps} req/s, ${conc} workers) for ${duration}s..." >&2
    hey -c "$conc" -z "${duration}s" -disable-keepalive \
        "$EXPORTER_URL/snmp?module=$module&target=$TARGET" >/dev/null 2>&1
}

# Run benchmark and capture results
run_benchmark() {
    local module=$1
    local rps=${2:-$DEFAULT_RPS}
    local duration=${3:-$DEFAULT_DURATION}
    local output_file="$RESULTS_DIR/${module}_$(date +%Y%m%d_%H%M%S).txt"

    check_exporter
    verify_module "$module"

    log "Benchmarking: $module"
    log "  Rate: ${rps} req/s, Duration: ${duration}s"

    # Warmup
    warmup "$module" "$DEFAULT_WARMUP" "$rps"

    # Reset exporter metrics
    curl -s "$EXPORTER_URL/metrics" >/dev/null

    local conc=$(calc_concurrency "$rps")
    log "Running benchmark (${conc} concurrent workers)..."
    hey -c "$conc" -z "${duration}s" -disable-keepalive \
        "$EXPORTER_URL/snmp?module=$module&target=$TARGET" | tee "$output_file"

    log "Results saved to: $output_file"
    echo
}

# Capture CPU profile during load
# Set 4th arg to "restart" for fresh process (fair A/B comparison)
# Outputs only the profile file path to stdout; logs go to stderr
capture_profile() {
    local module=$1
    local duration=${2:-30}
    local rps=${3:-$DEFAULT_RPS}
    local fresh=${4:-}
    local profile_file="$RESULTS_DIR/${module}_cpu_$(date +%Y%m%d_%H%M%S).pprof"

    if [ "$fresh" = "restart" ]; then
        restart_exporter
    else
        check_exporter >&2
    fi
    verify_module "$module"

    log "Capturing CPU profile for: $module" >&2
    log "  Duration: ${duration}s at ${rps} req/s" >&2

    # Warmup first
    warmup "$module" "$DEFAULT_WARMUP" "$rps"

    # Start load in background
    local conc=$(calc_concurrency "$rps")
    log "Starting sustained load (${conc} workers)..." >&2
    hey -c "$conc" -z "$((duration + 5))s" -disable-keepalive \
        "$EXPORTER_URL/snmp?module=$module&target=$TARGET" >/dev/null 2>&1 &
    local load_pid=$!

    sleep 2  # Let load stabilize

    # Capture profile
    log "Capturing ${duration}s CPU profile..." >&2
    curl -s "$EXPORTER_URL/debug/pprof/profile?seconds=$duration" -o "$profile_file"

    # Stop load
    kill $load_pid 2>/dev/null || true
    wait $load_pid 2>/dev/null || true

    # Capture metrics
    local metrics_file="${profile_file%.pprof}.metrics"
    capture_metrics "$metrics_file"

    log "Profile saved: $profile_file" >&2
    echo "$profile_file"
}

# Capture heap/allocs profile
capture_heap() {
    local module=$1
    local rps=${2:-$DEFAULT_RPS}
    local heap_file="$RESULTS_DIR/${module}_heap_$(date +%Y%m%d_%H%M%S).pprof"

    check_exporter
    verify_module "$module"

    log "Capturing heap profile for: $module at ${rps} req/s"

    # Generate some load first
    warmup "$module" 10 "$rps"

    # Capture heap
    curl -s "$EXPORTER_URL/debug/pprof/heap" -o "$heap_file"

    log "Heap profile saved: $heap_file"
    log "Analyze with: go tool pprof -http=:8080 $heap_file"
    echo "$heap_file"
}

# A/B comparison between two modules
compare() {
    local mod1=$1
    local mod2=$2
    local rps=${3:-$DEFAULT_RPS}
    local duration=${4:-30}

    log "===== A/B COMPARISON: $mod1 vs $mod2 ====="
    log "Rate: ${rps} req/s, Duration: ${duration}s each"
    log "(Fresh exporter process for each module)"
    echo

    # Capture profiles for both - restart exporter for fair comparison
    log "--- Profiling: $mod1 ---"
    local prof1=$(capture_profile "$mod1" "$duration" "$rps" restart)

    sleep 2

    log "--- Profiling: $mod2 ---"
    local prof2=$(capture_profile "$mod2" "$duration" "$rps" restart)

    echo
    log "===== COMPARISON COMPLETE ====="
    echo
    log "--- Metrics Summary ---"
    echo "$mod1:"
    show_metrics_summary "$mod1" "${prof1%.pprof}.metrics"
    echo "$mod2:"
    show_metrics_summary "$mod2" "${prof2%.pprof}.metrics"
    echo
    log "--- Profiles ---"
    log "  $prof1"
    log "  $prof2"
    echo
    log "Compare with:"
    log "  go tool pprof -http=:8080 $prof1"
    log "  go tool pprof -http=:8081 $prof2"
    echo
    log "Or diff:"
    log "  go tool pprof -base=$prof1 $prof2"

    stop_exporter
}

# Benchmark all core modules
benchmark_all() {
    local rps=${1:-$DEFAULT_RPS}
    local duration=${2:-30}

    log "===== BENCHMARKING ALL CORE MODULES ====="
    log "Modules: $CORE_MODULES"
    log "Rate: ${rps} req/s, Duration: ${duration}s each"
    log "(Fresh exporter process for each module)"
    echo

    local profiles=()

    for module in $CORE_MODULES; do
        log "--- $module ---"
        local prof=$(capture_profile "$module" "$duration" "$rps" restart)
        profiles+=("$module:$prof")
        sleep 2
    done

    echo
    log "===== ALL PROFILES CAPTURED ====="

    echo
    log "--- Metrics Summary ---"
    for p in "${profiles[@]}"; do
        local name="${p%%:*}"
        local file="${p##*:}"
        echo "$name:"
        show_metrics_summary "$name" "${file%.pprof}.metrics"
    done

    echo
    log "--- CPU Profile Summary (top 5) ---"
    for p in "${profiles[@]}"; do
        local name="${p%%:*}"
        local file="${p##*:}"
        echo
        echo "=== $name ==="
        go tool pprof -top -nodecount=5 "$file" 2>/dev/null | tail -n +5 | head -7
    done

    echo
    log "--- Profile Files ---"
    for p in "${profiles[@]}"; do
        echo "  $p"
    done

    stop_exporter
}

# Quick latency test without profiling
quick_test() {
    local module=$1
    local rps=${2:-50}
    local duration=${3:-10}

    check_exporter
    verify_module "$module"

    local conc=$(calc_concurrency "$rps")
    log "Quick test: $module (~${rps} req/s, ${conc} workers) for ${duration}s"
    hey -c "$conc" -z "${duration}s" -disable-keepalive \
        "$EXPORTER_URL/snmp?module=$module&target=$TARGET" 2>&1 | \
        grep -E "(Requests/sec|Average|Fastest|Slowest|Status code)"
}

usage() {
    cat <<EOF
Usage: $0 <command> [args]

Commands:
  run <module> [rps] [duration]     Run benchmark (default: 100 rps, 60s)
  profile <module> [duration] [rps] Capture CPU profile under load
  heap <module> [rps]               Capture heap profile
  compare <mod1> <mod2> [rps]       A/B compare two modules
  all [rps] [duration]              Profile all core modules
  quick <module> [rps] [duration]   Quick latency test
  stop                              Stop the exporter

Core modules: $CORE_MODULES

Environment:
  TARGET=$TARGET
  PORT=$PORT

Examples:
  $0 run baseline 100 60
  $0 profile hint_mib 30 100
  $0 compare baseline hint_mib 100
  $0 all 50 30
EOF
}

case "${1:-}" in
    run)
        [ -z "${2:-}" ] && { usage; exit 1; }
        run_benchmark "$2" "${3:-$DEFAULT_RPS}" "${4:-$DEFAULT_DURATION}"
        ;;
    profile)
        [ -z "${2:-}" ] && { usage; exit 1; }
        capture_profile "$2" "${3:-30}" "${4:-$DEFAULT_RPS}"
        ;;
    heap)
        [ -z "${2:-}" ] && { usage; exit 1; }
        capture_heap "$2" "${3:-$DEFAULT_RPS}"
        ;;
    compare)
        [ -z "${2:-}" ] || [ -z "${3:-}" ] && { usage; exit 1; }
        compare "$2" "$3" "${4:-$DEFAULT_RPS}" "${5:-30}"
        ;;
    all)
        benchmark_all "${2:-$DEFAULT_RPS}" "${3:-30}"
        ;;
    quick)
        [ -z "${2:-}" ] && { usage; exit 1; }
        quick_test "$2" "${3:-50}" "${4:-10}"
        ;;
    stop)
        stop_exporter
        ;;
    *)
        usage
        ;;
esac
