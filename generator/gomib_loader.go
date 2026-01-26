// Copyright 2026 The Prometheus Authors
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
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/golangsnmp/gomib"
)

// One entry in the tree of the MIB.
type Node struct {
	Oid               string
	subid             uint32
	Module            string
	Label             string
	Augments          string
	Children          []*Node
	Description       string
	Type              string
	Hint              string
	TextualConvention string
	FixedSize         int
	Units             string
	Access            string
	EnumValues        map[int]string

	Indexes      []string
	ImpliedIndex bool
}

// Copy returns a deep copy of the tree underneath the current Node.
func (n *Node) Copy() *Node {
	newNode := *n
	newNode.Children = make([]*Node, 0, len(n.Children))
	newNode.EnumValues = make(map[int]string, len(n.EnumValues))
	newNode.Indexes = make([]string, len(n.Indexes))
	copy(newNode.Indexes, n.Indexes)
	// Deep copy children and enums.
	for _, child := range n.Children {
		newNode.Children = append(newNode.Children, child.Copy())
	}
	for k, v := range n.EnumValues {
		newNode.EnumValues[k] = v
	}
	return &newNode
}

// mapBaseType converts gomib BaseType to snmp_exporter type strings.
func mapBaseType(base gomib.BaseType) string {
	switch base {
	case gomib.BaseInteger32:
		return "INTEGER"
	case gomib.BaseUnsigned32:
		return "UNSIGNED32"
	case gomib.BaseCounter32:
		return "COUNTER"
	case gomib.BaseCounter64:
		return "COUNTER64"
	case gomib.BaseGauge32:
		return "GAUGE"
	case gomib.BaseTimeTicks:
		return "TIMETICKS"
	case gomib.BaseIpAddress:
		return "IPADDR"
	case gomib.BaseOctetString:
		return "OCTETSTR"
	case gomib.BaseObjectIdentifier:
		return "OBJID"
	case gomib.BaseBits:
		return "BITSTRING"
	case gomib.BaseOpaque:
		return "OPAQUE"
	default:
		return "OTHER"
	}
}

// mapKindToType handles special node kinds (notifications, groups, etc.)
func mapKindToType(kind gomib.Kind) string {
	switch kind {
	case gomib.KindNotification:
		return "NOTIFTYPE"
	case gomib.KindGroup:
		return "OBJGROUP"
	case gomib.KindCompliance:
		return "MODCOMP"
	case gomib.KindCapabilities:
		return "AGENTCAP"
	case gomib.KindNode:
		return "OBJIDENTITY"
	default:
		return ""
	}
}

// mapAccess converts gomib Access to snmp_exporter access strings.
func mapAccess(access gomib.Access) string {
	switch access {
	case gomib.AccessReadOnly:
		return "ACCESS_READONLY"
	case gomib.AccessReadWrite:
		return "ACCESS_READWRITE"
	case gomib.AccessReadCreate:
		return "ACCESS_CREATE"
	case gomib.AccessWriteOnly:
		return "ACCESS_WRITEONLY"
	case gomib.AccessNotAccessible:
		return "ACCESS_NOACCESS"
	case gomib.AccessAccessibleForNotify:
		return "ACCESS_NOTIFY"
	default:
		return "unknown"
	}
}

// computeFixedSize extracts fixed size from type constraints.
// Returns the size if there is exactly one size range with min==max, 0 otherwise.
func computeFixedSize(sizes []gomib.Range) int {
	if len(sizes) != 1 {
		return 0
	}
	if sizes[0].Min != sizes[0].Max {
		return 0
	}
	return int(sizes[0].Min)
}

// Global MIB storage for getMIBTree.
var loadedMib gomib.Mib

// buildNodeFromGomib recursively converts a gomib Node to snmp_exporter Node.
func buildNodeFromGomib(gn gomib.Node) *Node {
	n := &Node{
		EnumValues: make(map[int]string),
	}

	// Use pre-computed OID from gomib
	n.subid = gn.Arc()
	n.Oid = gn.OID().String()

	// Set basic fields
	n.Label = gn.Name()
	if gn.Module() != nil {
		n.Module = gn.Module().Name()
	}

	// Determine type based on kind and object
	kind := gn.Kind()

	// Try to get type from object definition
	if obj := gn.Object(); obj != nil {
		if typ := obj.Type(); typ != nil {
			n.Type = mapBaseType(typ.EffectiveBase())

			// Get textual convention name
			if typ.IsTextualConvention() {
				n.TextualConvention = typ.Name()
			}
		}

		// Access
		n.Access = mapAccess(obj.Access())

		// Display hint
		n.Hint = obj.EffectiveDisplayHint()

		// Fixed size
		n.FixedSize = computeFixedSize(obj.EffectiveSizes())

		// Units
		n.Units = obj.Units()

		// Description
		n.Description = obj.Description()

		// Enum values
		for _, nv := range obj.EffectiveEnums() {
			n.EnumValues[int(nv.Value)] = nv.Label
		}

		// Augments
		if aug := obj.Augments(); aug != nil {
			n.Augments = aug.Name()
		}

		// Indexes
		for _, idx := range obj.Index() {
			n.Indexes = append(n.Indexes, idx.Object.Name())
			if idx.Implied {
				n.ImpliedIndex = true
			}
		}
	} else if kindType := mapKindToType(kind); kindType != "" {
		// Special node types
		n.Type = kindType
	} else {
		n.Type = "OTHER"
	}

	// Recurse into children
	children := gn.Children()
	n.Children = make([]*Node, 0, len(children))
	for _, child := range children {
		n.Children = append(n.Children, buildNodeFromGomib(child))
	}

	// Sort children by subid
	sort.Slice(n.Children, func(i, j int) bool {
		return n.Children[i].subid < n.Children[j].subid
	})

	return n
}

// getMibsDir returns the MIB directories as a string.
func getMibsDir(paths []string) string {
	if len(paths) == 1 && paths[0] == "" {
		return "/usr/share/snmp/mibs"
	}
	return strings.Join(paths, ":")
}

// initMIB loads MIBs using gomib. Returns parse errors/warnings.
func initMIB(logger *slog.Logger) (string, error) {
	mibsDir := getMibsDir(*userMibsDir)
	logger.Info("Loading MIBs", "from", mibsDir)

	// Build sources from directories
	dirs := strings.Split(mibsDir, ":")
	var sources []gomib.Source
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		src, err := gomib.DirTree(dir)
		if err != nil {
			logger.Warn("Could not open MIB directory", "dir", dir, "err", err)
			continue
		}
		sources = append(sources, src)
	}

	if len(sources) == 0 {
		return "", fmt.Errorf("no valid MIB directories found")
	}

	// Combine sources and load
	var source gomib.Source
	if len(sources) == 1 {
		source = sources[0]
	} else {
		source = gomib.Multi(sources...)
	}

	mib, err := gomib.Load(context.Background(), source, gomib.WithLogger(logger))
	if err != nil {
		return "", fmt.Errorf("failed to load MIBs: %w", err)
	}

	loadedMib = mib

	// Format diagnostics
	var parseOutput []string
	for _, diag := range mib.Diagnostics() {
		parseOutput = append(parseOutput, diag.Message)
	}
	for _, unres := range mib.Unresolved() {
		if unres.Kind == "import" {
			parseOutput = append(parseOutput, fmt.Sprintf("Cannot find module (%s): At line 0 in (unknown)", unres.Symbol))
		}
	}

	return strings.Join(parseOutput, "\n"), nil
}

// getMIBTree returns the converted MIB tree.
func getMIBTree() *Node {
	if loadedMib == nil {
		return &Node{EnumValues: make(map[int]string)}
	}

	root := loadedMib.Root()
	head := &Node{
		EnumValues: make(map[int]string),
	}

	// Build from root's children (skip the root itself which has no arc)
	children := root.Children()
	head.Children = make([]*Node, 0, len(children))
	for _, child := range children {
		head.Children = append(head.Children, buildNodeFromGomib(child))
	}

	// Sort children by subid
	sort.Slice(head.Children, func(i, j int) bool {
		return head.Children[i].subid < head.Children[j].subid
	})

	// Find the iso(1) node and return it as head (matches net-snmp behavior)
	for _, child := range head.Children {
		if child.subid == 1 {
			return child
		}
	}

	return head
}
