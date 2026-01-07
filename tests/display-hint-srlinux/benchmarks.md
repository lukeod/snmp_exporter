# Display-Hint Performance Benchmarks

Compares snmp_exporter performance between built-in TC handlers and display_hint-based parsing using a Nokia SR Linux container as the SNMP target.

## Prerequisites

- SR Linux container running via containerlab
- snmp_exporter built: `cd ../.. && make build`
- `hey` HTTP load tool: `sudo apt install hey`

## Quick Start

```bash
# Start SR Linux (if not running)
sudo containerlab deploy -t topology.clab.yml

# Generate config
./run.sh generate

# Quick latency comparison
./benchmark.sh quick baseline 100 10
./benchmark.sh quick hint_mib 100 10

# Full A/B profile comparison
./benchmark.sh compare baseline hint_mib 100
```

## Scripts

| Script | Purpose |
|--------|---------|
| [run.sh](run.sh) | Functional A/B tests - verifies output correctness |
| [benchmark.sh](benchmark.sh) | Performance profiling with pprof |
| [topology.clab.yml](topology.clab.yml) | Containerlab topology for SR Linux |
| [generator.yml](generator.yml) | Module definitions for test scenarios |

## Modules Under Test

| Module | Description |
|--------|-------------|
| `baseline` | Built-in TC handlers (PhysAddress48, DateAndTime) |
| `raw_octetstring` | Raw hex, no formatting |
| `hint_mib` | OctetString + `display_hint: @mib` |
| `hint_explicit` | OctetString + hardcoded hint strings |

All modules scrape identical OIDs, differing only in value rendering approach.

## Benchmark Commands

```bash
# Capture 30s CPU profile at ~100 req/s
./benchmark.sh profile <module> 30 100

# Compare two modules (captures both profiles)
./benchmark.sh compare baseline hint_mib 100

# Profile all core modules
./benchmark.sh all 100 30

# Heap profile after load
./benchmark.sh heap <module> 100
```

## Analyzing Results

```bash
# Interactive web UI
go tool pprof -http=:8080 ./benchmark-results/<profile>.pprof

# Diff two profiles
go tool pprof -base=./benchmark-results/baseline_*.pprof \
    ./benchmark-results/hint_mib_*.pprof

# Top functions text output
go tool pprof -top -nodecount=20 ./benchmark-results/<profile>.pprof
```

## Environment

- Target: `172.20.20.2:161` (SR Linux SNMPv2c)
- Exporter: `localhost:9116`
- SR Linux capacity: ~7,500 SNMP ops/sec sustained

Results in `./benchmark-results/`.

## Notes

- `compare` and `all` commands restart the exporter between modules for fair comparison (avoids GC/warmup bias)
- Single `profile` command reuses running exporter (faster for iterating on one module)
- Concurrency auto-calculated: `workers = target_rps / 10` (assumes ~100ms latency)
- Each profile run also captures `/metrics` data (scrape count, duration, GC stats, allocations)
