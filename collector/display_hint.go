// Copyright 2025 The Prometheus Authors
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

package collector

import (
	"strconv"
	"strings"
)

// hexDigits is a lookup table for uppercase hex digits.
const hexDigits = "0123456789ABCDEF"

// applyDisplayHint parses an RFC 2579 DISPLAY-HINT string and applies it to
// raw bytes in a single pass.
//
// Returns (result, true) on success, or ("", false) on any parse error.
// The caller should fall back to default formatting when ok is false.
//
// RFC 2579 Section 3.1 defines the octet-format specification:
//   - Optional '*' repeat indicator: first byte of value is repeat count
//   - Octet length: decimal digits specifying bytes to consume per application
//   - Format: 'd' decimal, 'x' hex, 'o' octal, 'a' ASCII, 't' UTF-8
//   - Optional separator: single character after each application
//   - Optional terminator: single character after repeat group (requires '*')
//
// The last format specification repeats until all data is exhausted (implicit
// repetition rule). Trailing separators are suppressed.
//
// Examples:
//   - "1d.1d.1d.1d" on [192,168,1,1] → "192.168.1.1"
//   - "1x:" on [0,26,43,60,77,94] → "00:1a:2b:3c:4d:5e"
//   - "255a" on [72,101,108,108,111] → "Hello"
func applyDisplayHint(hint string, data []byte) (string, bool) {
	if hint == "" || len(data) == 0 {
		return "", false
	}

	var result strings.Builder
	result.Grow(len(data) * 3) // Reasonable estimate for most formats

	hintPos := 0
	dataPos := 0

	// Track the start of the last spec for implicit repetition
	lastSpecStart := 0
	// Track whether the last spec consumes data (for infinite loop prevention)
	lastSpecConsumesByte := false

	for dataPos < len(data) {
		specStart := hintPos

		// If we've exhausted the hint, restart from the last spec (implicit repetition)
		if hintPos >= len(hint) {
			// Guard against infinite loop: if last spec doesn't consume data, bail
			if !lastSpecConsumesByte {
				return "", false
			}
			hintPos = lastSpecStart
			specStart = lastSpecStart
		}

		// (1) Optional '*' repeat indicator
		starPrefix := false
		if hintPos < len(hint) && hint[hintPos] == '*' {
			starPrefix = true
			hintPos++
		}

		// (2) Octet length - one or more decimal digits (required)
		if hintPos >= len(hint) || hint[hintPos] < '0' || hint[hintPos] > '9' {
			return "", false
		}

		take := 0
		for hintPos < len(hint) && hint[hintPos] >= '0' && hint[hintPos] <= '9' {
			take = take*10 + int(hint[hintPos]-'0')
			hintPos++
		}

		if take < 0 {
			return "", false
		}

		// (3) Format character (required)
		if hintPos >= len(hint) {
			return "", false
		}

		fmtChar := hint[hintPos]
		if fmtChar != 'd' && fmtChar != 'x' && fmtChar != 'o' && fmtChar != 'a' && fmtChar != 't' {
			return "", false
		}
		hintPos++

		// (4) Optional separator
		var sep byte
		hasSep := false
		if hintPos < len(hint) && (hint[hintPos] < '0' || hint[hintPos] > '9') && hint[hintPos] != '*' {
			sep = hint[hintPos]
			hasSep = true
			hintPos++
		}

		// (5) Optional terminator (only valid with starPrefix)
		var term byte
		hasTerm := false
		if starPrefix && hintPos < len(hint) && (hint[hintPos] < '0' || hint[hintPos] > '9') && hint[hintPos] != '*' {
			term = hint[hintPos]
			hasTerm = true
			hintPos++
		}

		// Remember this spec for implicit repetition
		lastSpecStart = specStart
		lastSpecConsumesByte = (take > 0) || starPrefix

		// Apply the spec to data
		repeatCount := 1
		if starPrefix && dataPos < len(data) {
			repeatCount = int(data[dataPos])
			dataPos++
		}

		for r := 0; r < repeatCount && dataPos < len(data); r++ {
			end := dataPos + take
			if end > len(data) || end < dataPos {
				end = len(data)
			}
			chunk := data[dataPos:end]

			switch fmtChar {
			case 'd':
				var val uint64
				for _, b := range chunk {
					val = (val << 8) | uint64(b)
				}
				var buf [20]byte
				result.Write(strconv.AppendUint(buf[:0], val, 10))
			case 'x':
				for _, b := range chunk {
					result.WriteByte(hexDigits[b>>4])
					result.WriteByte(hexDigits[b&0x0F])
				}
			case 'o':
				var val uint64
				for _, b := range chunk {
					val = (val << 8) | uint64(b)
				}
				var buf [22]byte
				result.Write(strconv.AppendUint(buf[:0], val, 8))
			case 'a', 't':
				result.Write(chunk)
			}
			dataPos = end

			// Emit separator (suppressed if at end of data or before terminator)
			if hasSep && dataPos < len(data) && (!hasTerm || r != repeatCount-1) {
				result.WriteByte(sep)
			}
		}

		// Emit terminator after repeat group
		if hasTerm && dataPos < len(data) {
			result.WriteByte(term)
		}
	}

	return result.String(), true
}
