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
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	gomib "github.com/golangsnmp/gomib"
	"github.com/golangsnmp/gomib/mib"
	"github.com/prometheus/common/promslog"

	"github.com/prometheus/snmp_exporter/config"
)

// testLoadMIBs loads the self-contained test MIBs from testdata/mibs/.
func testLoadMIBs(t *testing.T) *mib.Mib {
	t.Helper()
	src, err := gomib.Dir("testdata/mibs")
	if err != nil {
		t.Fatalf("Cannot open test MIB directory: %v", err)
	}
	m, err := gomib.Load(context.Background(),
		gomib.WithSource(src),
		gomib.WithResolverStrictness(mib.ResolverPermissive),
		gomib.WithDiagnosticConfig(mib.DiagnosticConfig{FailAt: mib.SeverityFatal}),
	)
	if err != nil {
		t.Fatalf("MIB loading failed: %v", err)
	}
	return m
}

func testLogger() *slog.Logger {
	return promslog.New(&promslog.Config{})
}

func findMetric(metrics []*config.Metric, name string) *config.Metric {
	for _, m := range metrics {
		if m.Name == name {
			return m
		}
	}
	return nil
}

func TestMinimizeOids(t *testing.T) {
	cases := []struct {
		in  []string
		out []string
	}{
		{
			in:  []string{"1.2.3.4", "1.2.3"},
			out: []string{"1.2.3"},
		},
		{
			in:  []string{"1.2.3", "1.2.3.4"},
			out: []string{"1.2.3"},
		},
		{
			in:  []string{"1.2.3", "1.2.3"},
			out: []string{"1.2.3"},
		},
		{
			in:  []string{"1.2.3", "1.2.4"},
			out: []string{"1.2.3", "1.2.4"},
		},
		{
			in:  []string{"1.2.3", "1.2.30"},
			out: []string{"1.2.3", "1.2.30"},
		},
	}
	for i, c := range cases {
		got := minimizeOids(c.in)
		if !reflect.DeepEqual(got, c.out) {
			t.Errorf("case %d: got %v, want %v", i, got, c.out)
		}
	}
}

func TestSanitizeLabelName(t *testing.T) {
	cases := []struct {
		in  string
		out string
	}{
		{"ifDescr", "ifDescr"},
		{"if-Descr", "if_Descr"},
		{"if.Descr", "if_Descr"},
		{"if Descr", "if_Descr"},
	}
	for i, c := range cases {
		got := sanitizeLabelName(c.in)
		if got != c.out {
			t.Errorf("case %d: got %q, want %q", i, got, c.out)
		}
	}
}

func TestMetricType(t *testing.T) {
	cases := []struct {
		in       string
		wantType string
		wantOk   bool
	}{
		{"gauge", "gauge", true},
		{"counter", "counter", true},
		{"OctetString", "OctetString", true},
		{"Bits", "Bits", true},
		{"EnumAsInfo", "EnumAsInfo", true},
		{"EnumAsStateSet", "EnumAsStateSet", true},
		{"DisplayString", "DisplayString", true},
		{"PhysAddress48", "PhysAddress48", true},
		{"InetAddressIPv4", "InetAddressIPv4", true},
		{"InetAddressIPv6", "InetAddressIPv6", true},
		{"InetAddress", "InetAddress", true},
		{"DateAndTime", "DateAndTime", true},
		{"Float", "Float", true},
		{"Double", "Double", true},
		{"NTPTimeStamp", "NTPTimeStamp", true},
		{"INVALID", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		gotType, gotOk := metricType(c.in)
		if gotType != c.wantType || gotOk != c.wantOk {
			t.Errorf("metricType(%q) = (%q, %v), want (%q, %v)", c.in, gotType, gotOk, c.wantType, c.wantOk)
		}
	}
}

func TestGenerateConfigModule_IfTable(t *testing.T) {
	m := testLoadMIBs(t)

	cfg := &ModuleConfig{
		Walk: []string{"EXPORTERTEST-MIB::testInterfaces", "EXPORTERTEST-MIB::testIfXTable"},
	}

	out, err := generateConfigModule(cfg, m, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	if len(out.Walk) == 0 {
		t.Error("expected walk OIDs")
	}
	if len(out.Metrics) == 0 {
		t.Fatal("expected metrics")
	}

	ifDescr := findMetric(out.Metrics, "testIfDescr")
	if ifDescr == nil {
		t.Fatal("expected testIfDescr metric")
	}
	if ifDescr.Type != "DisplayString" {
		t.Errorf("testIfDescr type = %q, want DisplayString", ifDescr.Type)
	}
	if len(ifDescr.Indexes) == 0 {
		t.Error("testIfDescr should have indexes")
	}

	ifIndex := findMetric(out.Metrics, "testIfIndex")
	if ifIndex == nil {
		t.Fatal("expected testIfIndex metric")
	}
	if ifIndex.Type != "gauge" {
		t.Errorf("testIfIndex type = %q, want gauge", ifIndex.Type)
	}

	ifInOctets := findMetric(out.Metrics, "testIfInOctets")
	if ifInOctets == nil {
		t.Fatal("expected testIfInOctets metric")
	}
	if ifInOctets.Type != "counter" {
		t.Errorf("testIfInOctets type = %q, want counter", ifInOctets.Type)
	}
}

func TestGenerateConfigModule_Scalar(t *testing.T) {
	m := testLoadMIBs(t)

	cfg := &ModuleConfig{
		Walk: []string{"testSysUpTime"},
	}

	out, err := generateConfigModule(cfg, m, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	if len(out.Get) == 0 {
		t.Error("expected scalar GET OID")
	}
	if len(out.Metrics) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(out.Metrics))
	}
	if out.Metrics[0].Name != "testSysUpTime" {
		t.Errorf("expected testSysUpTime, got %s", out.Metrics[0].Name)
	}
	if out.Metrics[0].Type != "gauge" {
		t.Errorf("testSysUpTime type = %q, want gauge", out.Metrics[0].Type)
	}
}

func TestGenerateConfigModule_WithLookups(t *testing.T) {
	m := testLoadMIBs(t)

	cfg := &ModuleConfig{
		Walk: []string{"EXPORTERTEST-MIB::testInterfaces"},
		Lookups: []*Lookup{
			{
				SourceIndexes: []string{"testIfIndex"},
				Lookup:        "EXPORTERTEST-MIB::testIfDescr",
			},
		},
	}

	out, err := generateConfigModule(cfg, m, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	hasLookup := false
	for _, metric := range out.Metrics {
		if len(metric.Lookups) > 0 {
			hasLookup = true
			if metric.Lookups[0].Labelname != "testIfDescr" {
				t.Errorf("expected lookup labelname testIfDescr, got %s", metric.Lookups[0].Labelname)
			}
			if metric.Lookups[0].Oid == "" {
				t.Error("lookup should have an OID")
			}
			break
		}
	}
	if !hasLookup {
		t.Error("expected at least one metric with a lookup")
	}
}

func TestGenerateConfigModule_WithOverrides(t *testing.T) {
	m := testLoadMIBs(t)

	cfg := &ModuleConfig{
		Walk: []string{"EXPORTERTEST-MIB::testInterfaces"},
		Overrides: map[string]MetricOverrides{
			"testIfType": {
				Type: "EnumAsInfo",
			},
			"testIfDescr": {
				Ignore: true,
			},
		},
	}

	out, err := generateConfigModule(cfg, m, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	for _, metric := range out.Metrics {
		if metric.Name == "testIfDescr" {
			t.Error("testIfDescr should be ignored")
		}
		if metric.Name == "testIfType" {
			if metric.Type != "EnumAsInfo" {
				t.Errorf("testIfType type = %q, want EnumAsInfo", metric.Type)
			}
		}
	}
}

func TestGenerateConfigModule_PhysAddress(t *testing.T) {
	m := testLoadMIBs(t)

	cfg := &ModuleConfig{
		Walk: []string{"EXPORTERTEST-MIB::testInterfaces"},
	}

	out, err := generateConfigModule(cfg, m, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	metric := findMetric(out.Metrics, "testIfPhysAddress")
	if metric == nil {
		t.Fatal("testIfPhysAddress not found")
	}
	if metric.Type != "PhysAddress48" {
		t.Errorf("testIfPhysAddress type = %q, want PhysAddress48", metric.Type)
	}
}

// testIfXEntry AUGMENTS testIfEntry, so columns under testIfXTable should
// inherit testIfEntry's indexes (testIfIndex).
func TestGenerateConfigModule_Augments(t *testing.T) {
	m := testLoadMIBs(t)

	cfg := &ModuleConfig{
		Walk: []string{"EXPORTERTEST-MIB::testIfXTable"},
	}

	out, err := generateConfigModule(cfg, m, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	metric := findMetric(out.Metrics, "testIfHCInOctets")
	if metric == nil {
		t.Fatal("testIfHCInOctets not found")
	}
	if metric.Type != "counter" {
		t.Errorf("testIfHCInOctets type = %q, want counter", metric.Type)
	}
	if len(metric.Indexes) == 0 {
		t.Fatal("testIfHCInOctets should have indexes inherited from testIfEntry")
	}
	if metric.Indexes[0].Labelname != "testIfIndex" {
		t.Errorf("expected testIfIndex as inherited index, got %q", metric.Indexes[0].Labelname)
	}
}

// testTargetAddrEntry has INDEX { IMPLIED testTargetAddrName }.
// Columns should have the implied flag set on the last index.
func TestGenerateConfigModule_ImpliedIndex(t *testing.T) {
	m := testLoadMIBs(t)

	cfg := &ModuleConfig{
		Walk: []string{"EXPORTERTEST-MIB::testTargetAddrTable"},
	}

	out, err := generateConfigModule(cfg, m, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	metric := findMetric(out.Metrics, "testTargetAddrTDomain")
	if metric == nil {
		t.Fatal("testTargetAddrTDomain not found")
	}
	if len(metric.Indexes) == 0 {
		t.Fatal("expected indexes")
	}
	lastIdx := metric.Indexes[len(metric.Indexes)-1]
	if !lastIdx.Implied {
		t.Error("last index should be implied")
	}
	if lastIdx.Labelname != "testTargetAddrName" {
		t.Errorf("implied index name = %q, want testTargetAddrName", lastIdx.Labelname)
	}
}

// testConnEntry has InetAddressType+InetAddress pairs in its indexes.
// The combined type logic should collapse (InetAddressType, InetAddress) into
// just (InetAddress).
func TestGenerateConfigModule_CombinedTypes(t *testing.T) {
	m := testLoadMIBs(t)

	cfg := &ModuleConfig{
		Walk: []string{"EXPORTERTEST-MIB::testConnTable"},
	}

	out, err := generateConfigModule(cfg, m, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	metric := findMetric(out.Metrics, "testConnState")
	if metric == nil {
		t.Fatal("testConnState not found")
	}

	// The raw indexes are:
	//   testConnLocalAddressType  (InetAddressType)
	//   testConnLocalAddress      (InetAddress)
	//   testConnLocalPort         (gauge)
	//   testConnRemAddressType    (InetAddressType)
	//   testConnRemAddress        (InetAddress)
	//   testConnRemPort           (gauge)
	//
	// After combined-type collapsing, each (InetAddressType, InetAddress)
	// pair should become just (InetAddress), so we expect 4 indexes:
	//   testConnLocalAddress, testConnLocalPort,
	//   testConnRemAddress, testConnRemPort
	indexNames := []string{}
	for _, idx := range metric.Indexes {
		indexNames = append(indexNames, idx.Labelname)
	}

	// InetAddressType indexes should have been removed.
	for _, name := range indexNames {
		if strings.HasSuffix(name, "AddressType") {
			t.Errorf("InetAddressType index %q should have been collapsed", name)
		}
	}

	// InetAddress indexes should remain.
	foundLocal := false
	foundRem := false
	for _, name := range indexNames {
		if name == "testConnLocalAddress" {
			foundLocal = true
		}
		if name == "testConnRemAddress" {
			foundRem = true
		}
	}
	if !foundLocal {
		t.Error("expected testConnLocalAddress in indexes")
	}
	if !foundRem {
		t.Error("expected testConnRemAddress in indexes")
	}
}

// Walking a testConnState column by its full OID with an instance suffix
// should classify it as an instance OID, generating a GET.
func TestGenerateConfigModule_InstanceOID(t *testing.T) {
	m := testLoadMIBs(t)

	// testConnState is at 1.3.6.1.4.1.99991.4.1.1.7.
	// Use a fake instance suffix.
	cfg := &ModuleConfig{
		Walk: []string{"1.3.6.1.4.1.99991.4.1.1.7.1.4.10.0.0.1.80.1.4.10.0.0.2.8080"},
	}

	out, err := generateConfigModule(cfg, m, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	if len(out.Get) == 0 {
		t.Error("expected instance OID to produce a GET")
	}
	// Should not produce a walk for this OID.
	for _, w := range out.Walk {
		if strings.HasPrefix(w, "1.3.6.1.4.1.99991.4.1.1.7.1") {
			t.Errorf("instance OID should not be walked, got walk %s", w)
		}
	}
}

// Static filters should restrict walked OIDs to specific indices.
func TestGenerateConfigModule_StaticFilter(t *testing.T) {
	m := testLoadMIBs(t)

	cfg := &ModuleConfig{
		Walk: []string{"EXPORTERTEST-MIB::testIfTable"},
		Filters: config.Filters{
			Static: []config.StaticFilter{
				{
					Targets: []string{"EXPORTERTEST-MIB::testIfTable"},
					Indices: []string{"1", "2"},
				},
			},
		},
	}

	out, err := generateConfigModule(cfg, m, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	ifTableND := m.Resolve("EXPORTERTEST-MIB::testIfTable")
	if ifTableND == nil {
		t.Fatal("could not resolve EXPORTERTEST-MIB::testIfTable")
	}
	ifTableOID := ifTableND.OID().String()

	// The testIfTable OID should not appear as a walk target (replaced by per-index GETs).
	for _, w := range out.Walk {
		if w == ifTableOID {
			t.Error("filtered OID should not be walked as a subtree")
		}
	}

	// Specific indices should appear as GETs.
	if len(out.Get) == 0 {
		t.Fatal("expected filtered indices to appear as GETs")
	}
	foundIdx1 := false
	foundIdx2 := false
	for _, g := range out.Get {
		if strings.Contains(g, ".1") {
			foundIdx1 = true
		}
		if strings.Contains(g, ".2") {
			foundIdx2 = true
		}
	}
	if !foundIdx1 || !foundIdx2 {
		t.Errorf("expected GETs for indices 1 and 2, got: %v", out.Get)
	}
}

// Lookups with drop_source_indexes should add a delete-marker lookup entry.
func TestGenerateConfigModule_DropSourceIndexes(t *testing.T) {
	m := testLoadMIBs(t)

	cfg := &ModuleConfig{
		Walk: []string{"EXPORTERTEST-MIB::testInterfaces"},
		Lookups: []*Lookup{
			{
				SourceIndexes:     []string{"testIfIndex"},
				Lookup:            "EXPORTERTEST-MIB::testIfDescr",
				DropSourceIndexes: true,
			},
		},
	}

	out, err := generateConfigModule(cfg, m, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	// Find a metric that has the lookup applied.
	for _, metric := range out.Metrics {
		if len(metric.Lookups) < 2 {
			continue
		}
		// The first lookup is the actual lookup, the second is the
		// delete-marker for the dropped source index.
		found := false
		for _, l := range metric.Lookups {
			if l.Labelname == "testIfIndex" && l.Oid == "" {
				found = true
			}
		}
		if found {
			return
		}
	}
	t.Error("expected a delete-marker lookup for dropped source index testIfIndex")
}

// InetAddress metrics without a preceding InetAddressType sibling should
// be demoted to OctetString.
func TestGenerateConfigModule_InetAddressDemotion(t *testing.T) {
	m := testLoadMIBs(t)

	// testConnState's table has InetAddress columns (testConnLocalAddress
	// at .2 preceded by testConnLocalAddressType at .1). These should stay
	// as InetAddress since the preceding column is InetAddressType.
	cfg := &ModuleConfig{
		Walk: []string{"EXPORTERTEST-MIB::testConnTable"},
	}

	out, err := generateConfigModule(cfg, m, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	// testConnState should have InetAddress indexes (not OctetString),
	// because the preceding column IS InetAddressType.
	metric := findMetric(out.Metrics, "testConnState")
	if metric == nil {
		t.Fatal("testConnState not found")
	}

	for _, idx := range metric.Indexes {
		if idx.Labelname == "testConnLocalAddress" {
			if idx.Type == "OctetString" {
				t.Error("testConnLocalAddress should NOT be demoted to OctetString (has preceding InetAddressType)")
			}
		}
	}
}

// classifyOID should return oidNotFound for completely unknown OIDs.
func TestClassifyOID_Unknown(t *testing.T) {
	m := testLoadMIBs(t)

	_, oidType := classifyOID("99.99.99.99", m, nil)
	if oidType != oidNotFound {
		t.Errorf("expected oidNotFound, got %d", oidType)
	}

	_, oidType = classifyOID("nonExistentObject", m, nil)
	if oidType != oidNotFound {
		t.Errorf("expected oidNotFound for bad name, got %d", oidType)
	}
}

// classifyOID should return oidScalar for scalar objects.
func TestClassifyOID_Scalar(t *testing.T) {
	m := testLoadMIBs(t)

	nd, oidType := classifyOID("testSysUpTime", m, nil)
	if oidType != oidScalar {
		t.Errorf("expected oidScalar, got %d", oidType)
	}
	if nd == nil {
		t.Fatal("expected non-nil node")
	}
}

// classifyOID should return oidSubtree for table/non-leaf OIDs.
func TestClassifyOID_Subtree(t *testing.T) {
	m := testLoadMIBs(t)

	_, oidType := classifyOID("EXPORTERTEST-MIB::testIfTable", m, nil)
	if oidType != oidSubtree {
		t.Errorf("expected oidSubtree, got %d", oidType)
	}
}

// Overrides with display_hint @mib should pull the hint from the MIB.
func TestGenerateConfigModule_DisplayHintAtMib(t *testing.T) {
	m := testLoadMIBs(t)

	cfg := &ModuleConfig{
		Walk: []string{"EXPORTERTEST-MIB::testInterfaces"},
		Overrides: map[string]MetricOverrides{
			"testIfPhysAddress": {
				DisplayHint: "@mib",
			},
		},
	}

	out, err := generateConfigModule(cfg, m, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	metric := findMetric(out.Metrics, "testIfPhysAddress")
	if metric == nil {
		t.Fatal("testIfPhysAddress not found")
	}
	// PhysAddress has DISPLAY-HINT "1x:".
	if metric.DisplayHint != "1x:" {
		t.Errorf("display_hint = %q, want %q", metric.DisplayHint, "1x:")
	}
}

// BITS-typed objects should have their named bits exposed as enum_values.
func TestGenerateConfigModule_BitsEnumValues(t *testing.T) {
	m := testLoadMIBs(t)

	cfg := &ModuleConfig{
		Walk: []string{"EXPORTERTEST-MIB::testErrorBits"},
	}

	out, err := generateConfigModule(cfg, m, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	metric := findMetric(out.Metrics, "testErrorBits")
	if metric == nil {
		t.Fatal("testErrorBits not found")
	}
	if metric.Type != "Bits" {
		t.Errorf("type = %q, want Bits", metric.Type)
	}
	if len(metric.EnumValues) == 0 {
		t.Fatal("expected BITS enum values")
	}
	// Spot-check a known named bit.
	if metric.EnumValues[0] != "linkDown" {
		t.Errorf("enum_values[0] = %q, want linkDown", metric.EnumValues[0])
	}
}

// Type overrides from generator.yml should apply to index objects, not just
// the metric itself.
func TestGenerateConfigModule_TypeOverrideOnIndex(t *testing.T) {
	m := testLoadMIBs(t)

	cfg := &ModuleConfig{
		Walk: []string{"EXPORTERTEST-MIB::testIfTable"},
		Overrides: map[string]MetricOverrides{
			"testIfIndex": {Type: "EnumAsInfo"},
		},
	}

	out, err := generateConfigModule(cfg, m, testLogger())
	if err != nil {
		t.Fatal(err)
	}

	// testIfDescr is a column in testIfTable with testIfIndex as its index.
	metric := findMetric(out.Metrics, "testIfDescr")
	if metric == nil {
		t.Fatal("testIfDescr not found")
	}
	if len(metric.Indexes) == 0 {
		t.Fatal("expected indexes on testIfDescr")
	}
	if metric.Indexes[0].Type != "EnumAsInfo" {
		t.Errorf("testIfIndex index type = %q, want EnumAsInfo", metric.Indexes[0].Type)
	}
}

// When a lookup name resolves to a node outside the metric's table,
// findLookupObject should prefer a same-table candidate.
func TestFindLookupObject_SameTable(t *testing.T) {
	m := testLoadMIBs(t)

	// testIfDescr is at 1.3.6.1.4.1.99991.2.1.1.2. A metric at
	// 1.3.6.1.4.1.99991.2.1.1.5 (testIfInOctets) should match
	// testIfDescr in the same table.
	lookupNode := m.Resolve("EXPORTERTEST-MIB::testIfDescr")
	if lookupNode == nil {
		t.Fatal("could not resolve EXPORTERTEST-MIB::testIfDescr")
	}

	obj := findLookupObject(m, "testIfDescr", "1.3.6.1.4.1.99991.2.1.1.5", lookupNode)
	if obj == nil {
		t.Fatal("findLookupObject returned nil")
	}
	if obj.Name() != "testIfDescr" {
		t.Errorf("got %q, want testIfDescr", obj.Name())
	}
	// Confirm it's the same object (same OID prefix as the metric).
	if !strings.HasPrefix(obj.Node().OID().String(), "1.3.6.1.4.1.99991.2.1.1.") {
		t.Errorf("expected same-table object, got OID %s", obj.Node().OID().String())
	}
}

// Qualified lookup names should return the resolved node directly,
// without disambiguation.
func TestFindLookupObject_Qualified(t *testing.T) {
	m := testLoadMIBs(t)

	lookupNode := m.Resolve("EXPORTERTEST-MIB::testIfDescr")
	if lookupNode == nil {
		t.Fatal("could not resolve EXPORTERTEST-MIB::testIfDescr")
	}

	// Qualified name bypasses same-table search.
	obj := findLookupObject(m, "EXPORTERTEST-MIB::testIfDescr", "9.9.9.9", lookupNode)
	if obj == nil {
		t.Fatal("findLookupObject returned nil")
	}
	if obj.Name() != "testIfDescr" {
		t.Errorf("got %q, want testIfDescr", obj.Name())
	}
}

func TestObjectFixedSize(t *testing.T) {
	m := testLoadMIBs(t)

	// testVarSizeObject has SYNTAX OCTET STRING (SIZE (1..255)),
	// which is not a fixed size.
	nd := m.Resolve("EXPORTERTEST-MIB::testVarSizeObject")
	if nd == nil {
		t.Fatal("could not resolve testVarSizeObject")
	}
	obj := nd.Object()
	if obj == nil {
		t.Fatal("not an object")
	}
	if got := objectFixedSize(obj); got != 0 {
		t.Errorf("variable-size object: got %d, want 0", got)
	}

	// testFixedSizeObject has SYNTAX OCTET STRING (SIZE (8)), which is fixed.
	nd = m.Resolve("EXPORTERTEST-MIB::testFixedSizeObject")
	if nd == nil {
		t.Fatal("could not resolve testFixedSizeObject")
	}
	obj = nd.Object()
	if obj == nil {
		t.Fatal("not an object")
	}
	if got := objectFixedSize(obj); got != 8 {
		t.Errorf("fixed-size object: got %d, want 8", got)
	}

	// testIfPhysAddress has PhysAddress (OCTET STRING with no SIZE), not fixed.
	nd = m.Resolve("EXPORTERTEST-MIB::testIfPhysAddress")
	if nd == nil {
		t.Fatal("could not resolve testIfPhysAddress")
	}
	obj = nd.Object()
	if obj == nil {
		t.Fatal("not an object")
	}
	if got := objectFixedSize(obj); got != 0 {
		t.Errorf("PhysAddress: got %d, want 0", got)
	}
}

// Walking an unknown OID should produce an error.
func TestGenerateConfigModule_UnknownWalkOID(t *testing.T) {
	m := testLoadMIBs(t)

	cfg := &ModuleConfig{
		Walk: []string{"99.99.99.99.99"},
	}

	_, err := generateConfigModule(cfg, m, testLogger())
	if err == nil {
		t.Error("expected error for unknown walk OID")
	}
}

// Referencing an unknown lookup should produce an error.
func TestGenerateConfigModule_UnknownLookup(t *testing.T) {
	m := testLoadMIBs(t)

	cfg := &ModuleConfig{
		Walk: []string{"EXPORTERTEST-MIB::testInterfaces"},
		Lookups: []*Lookup{
			{
				SourceIndexes: []string{"testIfIndex"},
				Lookup:        "totallyBogusObject",
			},
		},
	}

	_, err := generateConfigModule(cfg, m, testLogger())
	if err == nil {
		t.Error("expected error for unknown lookup")
	}
}
