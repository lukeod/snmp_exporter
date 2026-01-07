# display_hint A/B Tests with Nokia SR Linux

Tests for the `display_hint` feature using Nokia SR Linux as an SNMP target.
All core modules scrape the **same OIDs** with different rendering approaches.

## Quick Start

```bash
# 1. Start SR Linux container
sudo containerlab deploy -t topology.clab.yml

# 2. Build generator/exporter (from repo root)
cd ../.. && make build && cd tests/display-hint-srlinux

# 3. Generate config and run comparison
./run.sh all
```

## Test Structure

All core modules walk the same OID set with different rendering strategies:

| OID | TC | MIB Hint |
|-----|----|----|
| sysDescr, sysName | DisplayString | `255a` |
| ifDescr, ifName | DisplayString | `255a` |
| ifPhysAddress | PhysAddress | `1x:` |
| tmnxHwSwLastBoot | DateAndTime | `2d-1d-1d,1d:1d:1d.1d` |
| vRiaInetAddress | TmnxAddressAndPrefixAddress | (none) |
| tmnxHwBaseMacAddress | MacAddress | `1x:` |
| tmnxHwCLEI, tmnxHwMfgString, tmnxHwSerialNumber | SnmpAdminString | `255a` |

Note: `vRiaInetAddress` uses Nokia's `TmnxAddressAndPrefixAddress` TC which wraps
`InetAddress` (RFC 4001). Neither TC defines a DISPLAY-HINT, so `@mib` cannot resolve
a hint. Use explicit hints like `1d.1d.1d.1d` for IPv4 or `2x:2x:...` for IPv6.

MacAddress and SnmpAdminString have the same hints as PhysAddress and DisplayString
respectively, but are different TCs. These test that `@mib` resolution correctly
traverses the TC hierarchy.

### Core Modules

| Module | Strategy | Example Output |
|--------|----------|----------------|
| `baseline` | Built-in type handlers | `sysName="srl1"`, `ifPhysAddress="1A:CE:..."` |
| `raw_octetstring` | Force OctetString, no hint | `sysName="0x73726C31"` |
| `hint_mib` | OctetString + `@mib` hint | `sysName="srl1"` |
| `hint_explicit` | OctetString + hardcoded hints | `sysName="srl1"` |

### Comparison: sysName

```
baseline:        sysName{sysName="srl1"} 1
raw_octetstring: sysName{sysName="0x73726C31"} 1
hint_mib:        sysName{sysName="srl1"} 1
hint_explicit:   sysName{sysName="srl1"} 1
```

### Comparison: ifPhysAddress

```
baseline:        ifPhysAddress{...,ifPhysAddress="1A:CE:00:FF:00:20"} 1
raw_octetstring: ifPhysAddress{...,ifPhysAddress="0x1ACE00FF0020"} 1
hint_mib:        ifPhysAddress{...,ifPhysAddress="1A:CE:00:FF:00:20"} 1
```

### Comparison: tmnxHwSwLastBoot (DateAndTime)

```
baseline:        tmnxHwSwLastBoot{...} 1.767767508e+09  (Unix timestamp)
raw_octetstring: tmnxHwSwLastBoot{...,tmnxHwSwLastBoot="0x07EA0107061F3000"} 1
hint_mib:        tmnxHwSwLastBoot{...,tmnxHwSwLastBoot="2026-1-7,6:31:48.0"} 1
hint_explicit:   tmnxHwSwLastBoot{...,tmnxHwSwLastBoot="2026-1-7,6:31:48.0"} 1
```

Note: Built-in `DateAndTime` type converts to Unix timestamp gauge. `display_hint`
preserves the formatted string as a label - different use cases.

### Comparison: tmnxHwBaseMacAddress (MacAddress TC)

```
baseline:        tmnxHwBaseMacAddress{...,tmnxHwBaseMacAddress="1A:CE:00:FF:00:00"} 1
raw_octetstring: tmnxHwBaseMacAddress{...,tmnxHwBaseMacAddress="0x1ACE00FF0000"} 1
hint_mib:        tmnxHwBaseMacAddress{...,tmnxHwBaseMacAddress="1A:CE:00:FF:00:00"} 1
hint_explicit:   tmnxHwBaseMacAddress{...,tmnxHwBaseMacAddress="1A:CE:00:FF:00:00"} 1
```

### Comparison: tmnxHwMfgString (SnmpAdminString TC)

```
baseline:        tmnxHwMfgString{...,tmnxHwMfgString="7220 IXR-D2L"} 1
raw_octetstring: tmnxHwMfgString{...,tmnxHwMfgString="0x3732323020495852..."} 1
hint_mib:        tmnxHwMfgString{...,tmnxHwMfgString="7220 IXR-D2L"} 1
hint_explicit:   tmnxHwMfgString{...,tmnxHwMfgString="7220 IXR-D2L"} 1
```

### Variation Modules

| Module | Tests | Hint |
|--------|-------|------|
| `variations_mac` | Dash separator | `1x-` → `1A-CE-00-FF-00-20` |
| `variations_mac_decimal` | Decimal format | `1d.` → `26.206.0.255.0.32` |
| `variations_hex_nosep` | No separator | `1x` → `1ACE00FF0020` |
| `variations_ipv6` | IPv6 explicit | `2x:2x:...` → `3FFF:0172:...` |

### Lookup Modules

| Module | Strategy |
|--------|----------|
| `lookup_baseline` | Default lookup behavior |
| `lookup_raw` | Force OctetString on lookup source |
| `lookup_hint` | `display_hint: "@mib"` on lookup |

## Usage

```bash
# Generate snmp.yml
./run.sh generate

# Run full comparison
./run.sh compare

# Query specific module
./run.sh query baseline
./run.sh query raw_octetstring

# Show specific metric across all core modules
./run.sh diff sysName
./run.sh diff ifPhysAddress
./run.sh diff tmnxHwSwLastBoot

# Show OID metadata (TC, DISPLAY-HINT) from MIB
./run.sh info ifPhysAddress
./run.sh info vRiaInetAddress

# Run everything
./run.sh all
```

## Files

```
generator.yml      # All test modules
snmp.yml           # Generated config (gitignored)
mibs/              # Complete MIB corpus (Nokia + dependencies)
topology.clab.yml  # Containerlab topology
run.sh             # Test runner
```

The `mibs/` directory contains a minimal complete set of MIBs required for generation
(42 files: Nokia TIMETRA-* MIBs plus standard dependencies like IF-MIB, SNMPv2-*, etc.).

## MIB DISPLAY-HINTs Available

| TC | Hint | Format |
|----|------|--------|
| DisplayString | `255a` | ASCII text |
| PhysAddress | `1x:` | Hex bytes, colon separator |
| DateAndTime | `2d-1d-1d,1d:1d:1d.1d` | Year-Mon-Day,Hr:Min:Sec.Dsec |
| InetAddressIPv4 | `1d.1d.1d.1d` | Decimal dotted |
| InetAddressIPv6 | `2x:2x:2x:2x:2x:2x:2x:2x` | Hex pairs, colon separator |
