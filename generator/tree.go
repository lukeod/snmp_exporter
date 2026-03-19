// Copyright 2018 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/golangsnmp/gomib/mib"
	"github.com/prometheus/snmp_exporter/config"
)

// These types have one following the other.
// We need to check indexes and sequences have them
// in the right order, so the exporter can handle them.
//
// The original net-snmp generator also had "InetAddressMissingSize" here,
// which was a net-snmp artifact for InetAddress without a SIZE constraint.
// gomib resolves InetAddress through its type chain and never produces that
// type name, so it is not needed.
var combinedTypes = map[string]string{
	"InetAddress": "InetAddressType",
	"LldpPortId":  "LldpPortIdSubtype",
}

// Include both ASCII and UTF-8 in DisplayString, even though DisplayString
// is technically only ASCII.
var displayStringRe = regexp.MustCompile(`^\d+[at]$`)

// metricType validates a Prometheus metric type string for use in
// generator.yml overrides. The set of accepted types here must stay
// in sync with the types returned by objectMetricType.
func metricType(t string) (string, bool) {
	if _, ok := combinedTypes[t]; ok {
		return t, true
	}
	switch t {
	case "gauge":
		return "gauge", true
	case "counter":
		return "counter", true
	case "OctetString":
		return "OctetString", true
	case "Bits":
		return "Bits", true
	case "InetAddressIPv4", "InetAddressIPv6":
		return t, true
	case "PhysAddress48", "DisplayString", "Float", "Double":
		return t, true
	case "DateAndTime", "ParseDateAndTime", "NTPTimeStamp":
		return t, true
	case "EnumAsInfo", "EnumAsStateSet":
		return t, true
	default:
		return "", false
	}
}

// objectMetricType determines the Prometheus metric type for a gomib Object,
// applying TC-based and hint-based type promotion. The set of types returned
// here must stay in sync with what metricType accepts for overrides.
func objectMetricType(obj *mib.Object) (string, bool) {
	if obj.Type() == nil {
		return "", false
	}

	tcName := objectTCName(obj)
	hint := obj.EffectiveDisplayHint()

	// TC-based type promotion.
	switch tcName {
	case "DisplayString":
		return "DisplayString", true
	case "PhysAddress":
		return "PhysAddress48", true
	case "Float", "Double":
		return tcName, true
	case "DateAndTime":
		return "DateAndTime", true
	case "NTPTimeStamp":
		return "NTPTimeStamp", true
	case "InetAddressIPv4", "InetAddressIPv6", "InetAddress":
		return tcName, true
	case "LldpPortId":
		return tcName, true
	}

	// Hint-based type promotion.
	// RFC 2579
	if hint == "1x:" {
		return "PhysAddress48", true
	}
	if displayStringRe.MatchString(hint) {
		return "DisplayString", true
	}

	// Base type mapping.
	switch obj.Type().EffectiveBase() {
	case mib.BaseInteger32, mib.BaseUnsigned32, mib.BaseGauge32, mib.BaseTimeTicks:
		return "gauge", true
	case mib.BaseCounter32, mib.BaseCounter64:
		return "counter", true
	case mib.BaseOctetString, mib.BaseObjectIdentifier, mib.BaseOpaque:
		return "OctetString", true
	case mib.BaseBits:
		return "Bits", true
	case mib.BaseIpAddress:
		return "InetAddressIPv4", true
	default:
		return "", false
	}
}

// objectTCName returns the textual convention name from the object's type chain.
func objectTCName(obj *mib.Object) string {
	for t := obj.Type(); t != nil; t = t.Parent() {
		if t.IsTextualConvention() {
			return t.Name()
		}
	}
	return ""
}

// objectFixedSize returns the fixed size of an object's type, or 0 if not fixed.
func objectFixedSize(obj *mib.Object) int {
	sizes := obj.EffectiveSizes()
	if len(sizes) == 1 && sizes[0].Min == sizes[0].Max && sizes[0].Min > 0 {
		return int(sizes[0].Min)
	}
	return 0
}

// enumMap converts a gomib NamedValue slice to a map[int]string.
func enumMap(nvs []mib.NamedValue) map[int]string {
	if len(nvs) == 0 {
		return nil
	}
	m := make(map[int]string, len(nvs))
	for _, nv := range nvs {
		m[int(nv.Value)] = nv.Label
	}
	return m
}

// objectEnumValues returns the enum or BITS named values for an object.
// BITS named values are stored separately from INTEGER enums in gomib,
// but the snmp_exporter config uses a single enum_values map for both.
func objectEnumValues(obj *mib.Object) map[int]string {
	if obj.Type() == nil {
		return nil
	}
	if obj.Type().EffectiveBase() == mib.BaseBits {
		return enumMap(obj.EffectiveBits())
	}
	return enumMap(obj.EffectiveEnums())
}

// promAccess reports whether the object's access level makes it eligible
// for metric generation. AccessNotAccessible is included because table
// index objects are often not-accessible but must appear in the generated
// config so the exporter can decode row instance suffixes.
func promAccess(a mib.Access) bool {
	switch a {
	case mib.AccessReadOnly, mib.AccessReadWrite, mib.AccessReadCreate, mib.AccessNotAccessible:
		return true
	default:
		return false
	}
}

type oidMetricType uint8

const (
	oidNotFound oidMetricType = iota
	oidScalar
	oidInstance
	oidSubtree
)

// classifyOID resolves an OID string and classifies it as scalar, instance, or subtree.
func classifyOID(oidStr string, m *mib.Mib, typeOverrides map[string]string) (*mib.Node, oidMetricType) {
	nd := m.Resolve(oidStr)
	if nd != nil {
		obj := nd.Object()
		if obj != nil {
			override := typeOverrides[nd.OID().String()]
			isValid := false
			if override != "" {
				_, isValid = metricType(override)
			} else {
				_, isValid = objectMetricType(obj)
			}
			if isValid && promAccess(obj.Access()) && obj.Kind() == mib.KindScalar {
				return nd, oidScalar
			}
		}
		return nd, oidSubtree
	}

	// Not a known name/OID. Try as numeric OID with instance suffix.
	oid, err := mib.ParseOID(oidStr)
	if err != nil {
		return nil, oidNotFound
	}
	nd = m.LongestPrefixByOID(oid)
	if nd == nil {
		return nil, oidNotFound
	}
	obj := nd.Object()
	if obj == nil {
		return nil, oidNotFound
	}
	_, isValid := resolveType(obj, typeOverrides)
	if !isValid || !promAccess(obj.Access()) || obj.Kind() != mib.KindColumn {
		return nil, oidNotFound
	}
	return nd, oidInstance
}

// trimDescription normalizes and truncates a description to its first sentence.
func trimDescription(desc string) string {
	s := strings.Join(strings.Fields(desc), " ")
	return strings.Split(s, ". ")[0]
}

// resolveType determines the metric type for a gomib Object, checking
// typeOverrides first, then falling back to objectMetricType.
func resolveType(obj *mib.Object, typeOverrides map[string]string) (string, bool) {
	if override, ok := typeOverrides[obj.Node().OID().String()]; ok {
		return metricType(override)
	}
	return objectMetricType(obj)
}

// processMetricNode builds a config.Metric from a gomib node, or returns nil if
// the node should be skipped (unsupported type, inaccessible, ignored, etc.).
func processMetricNode(nd *mib.Node, typeOverrides map[string]string, cfg *ModuleConfig, logger *slog.Logger) *config.Metric {
	obj := nd.Object()
	if obj == nil {
		return nil
	}

	oidStr := nd.OID().String()

	// Determine type (override takes priority).
	t, ok := resolveType(obj, typeOverrides)
	if !ok {
		return nil
	}

	if !promAccess(obj.Access()) {
		return nil
	}

	metricName := sanitizeLabelName(obj.Name())
	if cfg.Overrides[metricName].Ignore {
		return nil
	}

	metric := &config.Metric{
		Name:       metricName,
		Oid:        oidStr,
		Type:       t,
		Help:       trimDescription(obj.Description()) + " - " + oidStr,
		Indexes:    []*config.Index{},
		Lookups:    []*config.Lookup{},
		EnumValues: objectEnumValues(obj),
	}

	// Get indexes from parent row for column objects.
	var indexes []mib.IndexEntry
	if obj.Kind() == mib.KindColumn {
		if row := obj.Row(); row != nil {
			indexes = row.EffectiveIndexes()
		}
	}

	// Afi (Address family)
	prevType := ""
	// Safi (Subsequent address family, e.g. Multicast/Unicast)
	prev2Type := ""
	for _, idx := range indexes {
		if idx.Object == nil {
			logger.Warn("Could not find index object for node", "node", obj.Name(), "index", idx.TypeName)
			return nil
		}

		indexObj := idx.Object
		indexType, ok := resolveType(indexObj, typeOverrides)
		if !ok {
			logger.Warn("Can't handle index type on node", "node", obj.Name(), "index", indexObj.Name(), "type", indexObj.Type().EffectiveBase().String())
			return nil
		}

		index := &config.Index{
			Labelname:  indexObj.Name(),
			Type:       indexType,
			FixedSize:  objectFixedSize(indexObj),
			Implied:    idx.Implied,
			EnumValues: objectEnumValues(indexObj),
		}

		// Convert (InetAddressType,InetAddress) to (InetAddress).
		if subtype, ok := combinedTypes[index.Type]; ok {
			switch subtype {
			case prevType:
				metric.Indexes = metric.Indexes[:len(metric.Indexes)-1]
			case prev2Type:
				metric.Indexes = metric.Indexes[:len(metric.Indexes)-2]
			default:
				logger.Warn("Can't handle index type on node, missing preceding", "node", obj.Name(), "type", index.Type, "missing", subtype)
				return nil
			}
		}
		prev2Type = prevType
		prevType = objectTCName(indexObj)
		metric.Indexes = append(metric.Indexes, index)
	}

	return metric
}

func generateConfigModule(cfg *ModuleConfig, m *mib.Mib, logger *slog.Logger) (*config.Module, error) {
	out := &config.Module{}
	needToWalk := map[string]struct{}{}
	tableInstances := map[string][]string{}

	// Build type overrides map keyed by OID string.
	typeOverrides := map[string]string{}
	for name, params := range cfg.Overrides {
		if params.Type == "" {
			continue
		}
		nd := m.Resolve(name)
		if nd == nil {
			logger.Warn("Could not find node to override type", "node", name)
			continue
		}
		typeOverrides[nd.OID().String()] = params.Type
	}

	// Resolve walk OIDs.
	toWalk := []string{}
	for _, oid := range cfg.Walk {
		if strings.HasPrefix(oid, ".") {
			return nil, fmt.Errorf("invalid OID %s, prefix of '.' should be removed", oid)
		}
		nd := m.Resolve(oid)
		if nd != nil {
			toWalk = append(toWalk, nd.OID().String())
		} else {
			toWalk = append(toWalk, oid)
		}
	}
	toWalk = minimizeOids(toWalk)

	// Classify walk OIDs and find top-level metric nodes.
	metricNodes := map[*mib.Node]struct{}{}
	for _, oidStr := range toWalk {
		nd, oidType := classifyOID(oidStr, m, typeOverrides)
		switch oidType {
		case oidNotFound:
			return nil, fmt.Errorf("cannot find oid '%s' to walk", oidStr)
		case oidSubtree:
			needToWalk[oidStr] = struct{}{}
		case oidInstance:
			needToWalk[oidStr+"."] = struct{}{}
			ndOID := nd.OID().String()
			index := strings.Replace(oidStr, ndOID, "", 1)
			tableInstances[ndOID] = append(tableInstances[ndOID], index)
		case oidScalar:
			needToWalk[oidStr+".0."] = struct{}{}
		}
		if nd != nil {
			metricNodes[nd] = struct{}{}
		}
	}

	// Sort metrics by OID string for deterministic output. String comparison
	// matches net-snmp's ordering, making it simpler to diff against the
	// existing generator during migration.
	sortedNodes := make([]*mib.Node, 0, len(metricNodes))
	for nd := range metricNodes {
		sortedNodes = append(sortedNodes, nd)
	}
	sort.Slice(sortedNodes, func(i, j int) bool {
		return sortedNodes[i].OID().String() < sortedNodes[j].OID().String()
	})

	// Walk metric subtrees and collect usable metrics.
	for _, metricNode := range sortedNodes {
		for nd := range metricNode.Subtree() {
			metric := processMetricNode(nd, typeOverrides, cfg, logger)
			if metric != nil {
				out.Metrics = append(out.Metrics, metric)
			}
		}
	}

	// Build a map of OIDs targeted by static filters.
	filterMap := map[string][]string{}
	for _, filter := range cfg.Filters.Static {
		for _, oid := range filter.Targets {
			nd := m.Resolve(oid)
			if nd != nil {
				oid = nd.OID().String()
			}
			filterMap[oid] = filter.Indices
		}
	}

	// Build a list of lookup labels which are required as index.
	requiredAsIndex := []string{}
	for _, lookup := range cfg.Lookups {
		requiredAsIndex = append(requiredAsIndex, lookup.SourceIndexes...)
	}

	// Apply lookups.
	for _, metric := range out.Metrics {
		toDelete := []string{}

		for _, lookup := range cfg.Lookups {
			foundIndexes := 0
			for _, index := range metric.Indexes {
				for _, lookupIndex := range lookup.SourceIndexes {
					if index.Labelname == lookupIndex {
						foundIndexes++
					}
				}
			}
			if foundIndexes != len(lookup.SourceIndexes) {
				continue
			}

			lookupNode := m.Resolve(lookup.Lookup)
			if lookupNode == nil {
				return nil, fmt.Errorf("unknown index '%s'", lookup.Lookup)
			}
			lookupObj := findLookupObject(m, lookup.Lookup, metric.Oid, lookupNode)
			if lookupObj == nil {
				return nil, fmt.Errorf("unknown index '%s' (not an object)", lookup.Lookup)
			}

			lookupOID := lookupObj.Node().OID().String()
			var typ string
			if override, ok := typeOverrides[lookupOID]; ok {
				typ, _ = metricType(override)
			}
			if typ == "" {
				var ok bool
				typ, ok = objectMetricType(lookupObj)
				if !ok {
					return nil, fmt.Errorf("unknown index type for %s", lookup.Lookup)
				}
			}

			l := &config.Lookup{
				Labelname: sanitizeLabelName(lookupObj.Name()),
				Type:      typ,
				Oid:       lookupOID,
			}

			// Handle display_hint for lookup.
			if lookup.DisplayHint != "" {
				if lookup.DisplayHint == "@mib" {
					if h := lookupObj.EffectiveDisplayHint(); h != "" {
						l.DisplayHint = h
					} else {
						logger.Warn("display_hint @mib specified but MIB has no DISPLAY-HINT", "lookup", lookup.Lookup)
					}
				} else {
					l.DisplayHint = lookup.DisplayHint
				}
			}

			for _, oldIndex := range lookup.SourceIndexes {
				l.Labels = append(l.Labels, sanitizeLabelName(oldIndex))
			}
			metric.Lookups = append(metric.Lookups, l)

			// If lookup label is used as source index in another lookup,
			// we need to add this new label as another index.
			if slices.Contains(requiredAsIndex, l.Labelname) {
				idx := &config.Index{Labelname: l.Labelname, Type: l.Type}
				metric.Indexes = append(metric.Indexes, idx)
			}

			// Make sure we walk the lookup OID(s).
			if len(tableInstances[metric.Oid]) > 0 {
				for _, index := range tableInstances[metric.Oid] {
					needToWalk[lookupOID+index+"."] = struct{}{}
				}
			} else {
				needToWalk[lookupOID] = struct{}{}
			}

			// Apply the same filter to metric.Oid if the lookup OID is filtered.
			indices, found := filterMap[lookupOID]
			if found {
				delete(needToWalk, metric.Oid)
				for _, index := range indices {
					needToWalk[metric.Oid+"."+index+"."] = struct{}{}
				}
			}

			if lookup.DropSourceIndexes {
				toDelete = append(toDelete, lookup.SourceIndexes...)
			}
		}

		for _, l := range toDelete {
			metric.Lookups = append(metric.Lookups, &config.Lookup{
				Labelname: sanitizeLabelName(l),
			})
		}
	}

	// Ensure index label names are sane.
	for _, metric := range out.Metrics {
		for _, index := range metric.Indexes {
			index.Labelname = sanitizeLabelName(index.Labelname)
		}
	}

	// Check that the object before an InetAddress is an InetAddressType.
	// If not, change it to an OctetString.
	for _, metric := range out.Metrics {
		if metric.Type != "InetAddress" {
			continue
		}
		metricOID, err := mib.ParseOID(metric.Oid)
		if err != nil || len(metricOID) == 0 {
			continue
		}
		prevOID := make(mib.OID, len(metricOID))
		copy(prevOID, metricOID)
		prevOID[len(prevOID)-1]--

		prevNode := m.NodeByOID(prevOID)
		prevIsInetAddrType := false
		if prevNode != nil {
			if prevObj := prevNode.Object(); prevObj != nil {
				prevIsInetAddrType = objectTCName(prevObj) == "InetAddressType"
			}
		}
		if !prevIsInetAddrType {
			metric.Type = "OctetString"
		} else {
			prevOIDStr := prevOID.String()
			if len(tableInstances[metric.Oid]) > 0 {
				for _, index := range tableInstances[metric.Oid] {
					needToWalk[prevOIDStr+index+"."] = struct{}{}
				}
			} else {
				needToWalk[prevOIDStr] = struct{}{}
			}
		}
	}

	// Apply module config overrides to their corresponding metrics.
	for name, params := range cfg.Overrides {
		for _, metric := range out.Metrics {
			if name != metric.Name && name != metric.Oid {
				continue
			}
			metric.RegexpExtracts = params.RegexpExtracts
			metric.DateTimePattern = params.DateTimePattern
			metric.Offset = params.Offset
			metric.Scale = params.Scale
			if params.Help != "" {
				metric.Help = params.Help
			}
			if params.Name != "" {
				metric.Name = params.Name
			}
			if params.DisplayHint != "" {
				if params.DisplayHint == "@mib" {
					nd := m.Resolve(metric.Oid)
					if nd != nil {
						if obj := nd.Object(); obj != nil && obj.EffectiveDisplayHint() != "" {
							metric.DisplayHint = obj.EffectiveDisplayHint()
						} else {
							logger.Warn("display_hint @mib specified but MIB has no DISPLAY-HINT", "metric", metric.Oid)
						}
					}
				} else {
					metric.DisplayHint = params.DisplayHint
				}
			}
		}
	}

	// Apply static filters.
	for _, filter := range cfg.Filters.Static {
		for _, oid := range filter.Targets {
			nd := m.Resolve(oid)
			if nd != nil {
				oid = nd.OID().String()
			}
			delete(needToWalk, oid)
			for _, index := range filter.Indices {
				needToWalk[oid+"."+index+"."] = struct{}{}
			}
		}
	}

	out.Filters = cfg.Filters.Dynamic

	// Separate Walk and Get OIDs.
	oids := []string{}
	for k := range needToWalk {
		oids = append(oids, k)
	}
	for _, k := range minimizeOids(oids) {
		if k[len(k)-1:] == "." {
			out.Get = append(out.Get, k[:len(k)-1])
		} else {
			out.Walk = append(out.Walk, k)
		}
	}
	return out, nil
}

// findLookupObject resolves a lookup name to a *mib.Object, preferring one in
// the same table as the metric OID for disambiguation of duplicate names.
func findLookupObject(m *mib.Mib, lookupName string, metricOIDStr string, lookupNode *mib.Node) *mib.Object {
	obj := lookupNode.Object()
	if obj == nil {
		return nil
	}
	// Qualified names are already unambiguous.
	if strings.Contains(lookupName, "::") {
		return obj
	}
	// Check if the resolved object is already in the same subtree.
	metricOIDParts := strings.Split(metricOIDStr, ".")
	if len(metricOIDParts) > 1 {
		prefix := strings.Join(metricOIDParts[:len(metricOIDParts)-1], ".")
		if strings.HasPrefix(obj.Node().OID().String(), prefix) {
			return obj
		}
		// Search for an alternative in the same subtree.
		for _, candidate := range m.Objects() {
			if candidate.Name() != lookupNode.Name() {
				continue
			}
			if strings.HasPrefix(candidate.Node().OID().String(), prefix) {
				return candidate
			}
		}
	}
	return obj
}

// Reduce a set of overlapping OID subtrees.
func minimizeOids(oids []string) []string {
	sort.Strings(oids)
	prevOid := ""
	minimized := []string{}
	for _, oid := range oids {
		if !strings.HasPrefix(oid+".", prevOid) || prevOid == "" {
			minimized = append(minimized, oid)
			prevOid = oid + "."
		}
	}
	return minimized
}

var invalidLabelCharRE = regexp.MustCompile(`[^a-zA-Z0-9_]`)

func sanitizeLabelName(name string) string {
	return invalidLabelCharRE.ReplaceAllString(name, "_")
}
