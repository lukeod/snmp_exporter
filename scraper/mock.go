// Copyright 2024 The Prometheus Authors
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

package scraper

import (
	"sync"

	"github.com/gosnmp/gosnmp"
)

func NewMockSNMPScraper(get map[string]gosnmp.SnmpPDU, walk map[string][]gosnmp.SnmpPDU) *mockSNMPScraper {
	return &mockSNMPScraper{
		GetResponses:  get,
		WalkResponses: walk,
		calls:         &callRecorder{},
	}
}

// callRecorder records Get and Walk calls. It is shared between a mock and
// its clones so tests can assert on calls made from any connection; the
// mutex makes recording safe from concurrent walk goroutines.
type callRecorder struct {
	mu   sync.Mutex
	get  []string
	walk []string
}

type mockSNMPScraper struct {
	GetResponses  map[string]gosnmp.SnmpPDU
	WalkResponses map[string][]gosnmp.SnmpPDU
	ConnectError  error
	CloseError    error

	calls *callRecorder
}

func (m *mockSNMPScraper) CallGet() []string {
	m.calls.mu.Lock()
	defer m.calls.mu.Unlock()
	return append([]string{}, m.calls.get...)
}

func (m *mockSNMPScraper) CallWalk() []string {
	m.calls.mu.Lock()
	defer m.calls.mu.Unlock()
	return append([]string{}, m.calls.walk...)
}

func (m *mockSNMPScraper) Get(oids []string) (*gosnmp.SnmpPacket, error) {
	pdus := make([]gosnmp.SnmpPDU, 0, len(oids))
	for _, oid := range oids {
		if response, exists := m.GetResponses[oid]; exists {
			pdus = append(pdus, response)
		} else {
			pdus = append(pdus, gosnmp.SnmpPDU{
				Name:  oid,
				Type:  gosnmp.NoSuchObject,
				Value: nil,
			})
		}
		m.calls.mu.Lock()
		m.calls.get = append(m.calls.get, oid)
		m.calls.mu.Unlock()
	}
	return &gosnmp.SnmpPacket{
		Variables: pdus,
		Error:     gosnmp.NoError,
	}, nil
}

func (m *mockSNMPScraper) WalkAll(baseOID string) ([]gosnmp.SnmpPDU, error) {
	m.calls.mu.Lock()
	m.calls.walk = append(m.calls.walk, baseOID)
	m.calls.mu.Unlock()
	if pdus, exists := m.WalkResponses[baseOID]; exists {
		return pdus, nil
	}
	return nil, nil
}

func (m *mockSNMPScraper) Connect() error {
	return m.ConnectError
}

func (m *mockSNMPScraper) Close() error {
	return m.CloseError
}

func (m *mockSNMPScraper) SetOptions(...func(*gosnmp.GoSNMP)) {
}

// Clone returns a new mock sharing the same response maps (safe for parallel
// reads) and the same call recorder, so calls made through clones are visible
// to the parent's CallGet/CallWalk.
func (m *mockSNMPScraper) Clone() SNMPScraper {
	return &mockSNMPScraper{
		GetResponses:  m.GetResponses,
		WalkResponses: m.WalkResponses,
		ConnectError:  m.ConnectError,
		CloseError:    m.CloseError,
		calls:         m.calls,
	}
}
