#!/bin/bash
# display_hint A/B comparison tests using Nokia SR Linux
#
# Prerequisites:
#   - containerlab installed
#   - SR Linux container running: sudo containerlab deploy -t topology.clab.yml
#   - Generator and exporter built: cd ../.. && make build
#
# Usage:
#   ./run.sh generate       # Generate snmp.yml from generator.yml
#   ./run.sh compare        # Run full A/B comparison
#   ./run.sh diff METRIC    # Compare specific metric across core modules
#   ./run.sh query MODULE   # Query a specific module
#   ./run.sh all            # Generate + compare

set -e
cd "$(dirname "$0")"

GENERATOR="../../generator/generator"
EXPORTER="../../snmp_exporter"
TARGET="${TARGET:-172.20.20.2}"
PORT="${PORT:-9116}"

CORE_MODULES="baseline raw_octetstring hint_mib hint_explicit"
VARIATION_MODULES="variations_mac variations_mac_decimal variations_hex_nosep variations_ipv6 variations_2byte_hex variations_octal variations_utf8"
LOOKUP_MODULES="lookup_baseline lookup_raw lookup_hint"

# Show OID metadata (TC, DISPLAY-HINT) using snmptranslate
# Uses -m +ALL to load all MIBs, -IR for random access lookup
oid_info() {
    local oid=$1
    local info
    info=$(MIBDIRS=+./mibs snmptranslate -m +ALL -Td -IR -OS "$oid" 2>/dev/null)
    local tc hint
    tc=$(echo "$info" | grep "TEXTUAL CONVENTION" | sed 's/.*TEXTUAL CONVENTION //')
    hint=$(echo "$info" | grep "DISPLAY-HINT" | sed 's/.*DISPLAY-HINT\s*//')
    if [ -n "$tc" ]; then
        printf "  TC: %-20s  DISPLAY-HINT: %s\n" "$tc" "${hint:-(none)}"
    fi
}

generate() {
    echo "Generating snmp.yml..."
    $GENERATOR generate -m mibs 2>&1 | grep -E "level=(INFO|WARN)" | sed 's/.*msg=/  /'
    echo "Done: snmp.yml"
}

start_exporter() {
    if curl -s "http://localhost:$PORT/metrics" >/dev/null 2>&1; then
        return 0
    fi
    pkill -f "snmp_exporter.*config.file=snmp.yml" 2>/dev/null || true
    sleep 0.5
    $EXPORTER --config.file=snmp.yml --web.listen-address=":$PORT" >/dev/null 2>&1 &
    sleep 1.5
}

stop_exporter() {
    pkill -f "snmp_exporter.*config.file=snmp.yml" 2>/dev/null || true
}

query() {
    local module=$1
    curl -s "http://localhost:$PORT/snmp?module=$module&target=$TARGET"
}

# Compare a specific metric across all core modules
diff_metric() {
    local metric=$1
    echo "=== $metric ==="
    oid_info "$metric"
    for mod in $CORE_MODULES; do
        printf "%-18s " "$mod:"
        query "$mod" | grep "^$metric{" | head -1 || echo "(not found)"
    done
    echo
}

compare() {
    echo "Starting exporter..."
    start_exporter
    trap stop_exporter EXIT

    echo
    echo "=============================================="
    echo "CORE MODULE COMPARISON (same OIDs, different rendering)"
    echo "=============================================="
    echo

    diff_metric "sysName"
    diff_metric "ifPhysAddress"
    diff_metric "tmnxHwSwLastBoot"
    diff_metric "vRiaInetAddress"

    echo "=============================================="
    echo "VARIATION MODULES (custom hint overrides)"
    echo "=============================================="
    echo
    echo "ifPhysAddress:"
    oid_info ifPhysAddress
    echo

    echo "=== MAC with dash separator (hint: 1x-) ==="
    query variations_mac | grep "^ifPhysAddress{" | head -1
    echo

    echo "=== MAC as decimal (hint: 1d.) ==="
    query variations_mac_decimal | grep "^ifPhysAddress{" | head -1
    echo

    echo "=== Hex no separator (hint: 1x) ==="
    query variations_hex_nosep | grep "^ifPhysAddress{" | head -1
    echo

    echo "=== 2-byte hex with space (hint: 2x ) ==="
    query variations_2byte_hex | grep "^ifPhysAddress{" | head -1
    echo

    echo "=== Octal format (hint: 1o.) ==="
    query variations_octal | grep "^ifPhysAddress{" | head -1
    echo

    echo "vRiaInetAddress:"
    oid_info vRiaInetAddress
    echo

    echo "=== IPv6 format (hint: 2x:2x:...) ==="
    query variations_ipv6 | grep "^vRiaInetAddress{" | head -3
    echo

    echo "sysName:"
    oid_info sysName
    echo

    echo "=== UTF-8 format (hint: 255t) ==="
    query variations_utf8 | grep "^sysName{" | head -1
    echo

    echo "=============================================="
    echo "LOOKUP COMPARISON (ifDescr used as label)"
    echo "=============================================="
    echo
    echo "ifDescr (lookup source):"
    oid_info ifDescr
    echo

    echo "=== lookup_baseline (default type handling) ==="
    query lookup_baseline | grep "^ifHCInOctets{" | head -1
    echo

    echo "=== lookup_raw (force OctetString, no hint) ==="
    query lookup_raw | grep "^ifHCInOctets{" | head -1
    echo

    echo "=== lookup_hint (OctetString + display_hint: @mib) ==="
    query lookup_hint | grep "^ifHCInOctets{" | head -1
    echo
}

list_modules() {
    echo "Core modules:      $CORE_MODULES"
    echo "Variation modules: $VARIATION_MODULES"
    echo "Lookup modules:    $LOOKUP_MODULES"
}

case "${1:-}" in
    generate)
        generate
        ;;
    info)
        if [ -z "${2:-}" ]; then
            echo "Usage: $0 info <oid>"
            echo "Example: $0 info ifPhysAddress"
            echo "         $0 info vRiaInetAddress"
            exit 1
        fi
        MIBDIRS=+./mibs snmptranslate -m +ALL -Td -IR -OS "$2" 2>/dev/null
        ;;
    compare)
        compare
        ;;
    diff)
        if [ -z "${2:-}" ]; then
            echo "Usage: $0 diff <metric>"
            echo "Example: $0 diff sysName"
            exit 1
        fi
        start_exporter
        trap stop_exporter EXIT
        diff_metric "$2"
        ;;
    query)
        if [ -z "${2:-}" ]; then
            echo "Usage: $0 query <module>"
            list_modules
            exit 1
        fi
        start_exporter
        trap stop_exporter EXIT
        query "$2"
        ;;
    all)
        generate
        compare
        ;;
    stop)
        stop_exporter
        echo "Exporter stopped"
        ;;
    modules)
        list_modules
        ;;
    *)
        echo "Usage: $0 {generate|compare|diff|query|info|all|stop|modules}"
        echo
        echo "Commands:"
        echo "  generate       - Generate snmp.yml from generator.yml"
        echo "  compare        - Run full A/B comparison across all modules"
        echo "  diff <metric>  - Compare specific metric across core modules"
        echo "  query <module> - Query a specific module (raw output)"
        echo "  info <oid>     - Show OID metadata (TC, DISPLAY-HINT, etc.)"
        echo "  all            - Generate then compare"
        echo "  stop           - Stop the exporter"
        echo "  modules        - List all modules"
        echo
        echo "Environment:"
        echo "  TARGET=$TARGET  (SR Linux IP)"
        echo "  PORT=$PORT      (exporter port)"
        echo
        list_modules
        ;;
esac
