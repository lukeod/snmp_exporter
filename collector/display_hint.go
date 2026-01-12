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

// smallBufferSize is the threshold for using stack-allocated buffers.
// Covers common cases: IPv4 (15), MAC (17), IPv6 (39), UUID (36).
const smallBufferSize = 64

// estimateOutputSize returns a reasonable upper bound for the formatted output size.
// It scans for format characters to choose an appropriate multiplier, avoiding the
// 4x over-allocation that would occur for ASCII/UTF-8 text formats.
func estimateOutputSize(hint string, dataLen int) int {
	// Scan backwards for the last format character (most likely to repeat)
	for i := len(hint) - 1; i >= 0; i-- {
		switch hint[i] {
		case 'a', 't':
			// Text formats: 1 byte → 1 char, plus small separator allowance
			return dataLen + dataLen/8 + 1
		case 'x':
			// Hex: 1 byte → 2 chars, plus separator allowance
			return dataLen*3 + 1
		case 'd', 'o':
			// Decimal/octal: worst case ~3 chars per byte plus separators
			return dataLen*4 + 1
		}
	}
	return dataLen*4 + 1
}

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

	estimatedSize := estimateOutputSize(hint, len(data))

	// Use stack-allocated buffer for small outputs (common case: IPs, MACs, UUIDs).
	// For larger outputs, use strings.Builder which can return its internal buffer
	// without copying via unsafe (avoiding double allocation).
	if estimatedSize > smallBufferSize {
		return applyDisplayHintLarge(hint, data, estimatedSize)
	}

	var stackBuf [smallBufferSize]byte
	result := stackBuf[:0]

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
		if hintPos >= len(hint) || !isDigit(hint[hintPos]) {
			// Parse error: expected digits
			return "", false
		}

		take := 0
		for hintPos < len(hint) && isDigit(hint[hintPos]) {
			take = take*10 + int(hint[hintPos]-'0')
			hintPos++
		}

		if take < 0 {
			// Overflow wrapped to negative
			return "", false
		}

		// (3) Format character (required)
		if hintPos >= len(hint) {
			// Parse error: expected format character
			return "", false
		}

		fmtChar := hint[hintPos]
		if fmtChar != 'd' && fmtChar != 'x' && fmtChar != 'o' && fmtChar != 'a' && fmtChar != 't' {
			// Invalid format character
			return "", false
		}
		hintPos++

		// (4) Optional separator
		var sep byte
		hasSep := false
		if hintPos < len(hint) && !isDigit(hint[hintPos]) && hint[hintPos] != '*' {
			sep = hint[hintPos]
			hasSep = true
			hintPos++
		}

		// (5) Optional terminator (only valid with starPrefix)
		var term byte
		hasTerm := false
		if starPrefix && hintPos < len(hint) && !isDigit(hint[hintPos]) && hint[hintPos] != '*' {
			term = hint[hintPos]
			hasTerm = true
			hintPos++
		}

		// Remember this spec for implicit repetition
		lastSpecStart = specStart
		// A spec consumes data if take > 0, or if starPrefix (consumes repeat count byte)
		lastSpecConsumesByte = (take > 0) || starPrefix

		// Apply the spec to data
		repeatCount := 1
		if starPrefix && dataPos < len(data) {
			repeatCount = int(data[dataPos])
			dataPos++
		}

		for r := 0; r < repeatCount && dataPos < len(data); r++ {
			end := dataPos + take
			if end > len(data) || end < dataPos { // catch overflow
				end = len(data)
			}
			chunk := data[dataPos:end]

			// Format the chunk
			switch fmtChar {
			case 'd':
				// Big-endian unsigned integer
				var val uint64
				for _, b := range chunk {
					val = (val << 8) | uint64(b)
				}
				result = strconv.AppendUint(result, val, 10)
			case 'x':
				// Hex: 2 chars per byte using lookup table
				for _, b := range chunk {
					result = append(result, hexDigits[b>>4], hexDigits[b&0x0F])
				}
			case 'o':
				// Big-endian octal
				var val uint64
				for _, b := range chunk {
					val = (val << 8) | uint64(b)
				}
				result = strconv.AppendUint(result, val, 8)
			case 'a', 't':
				// ASCII/UTF-8 - append bytes directly
				result = append(result, chunk...)
			}
			dataPos = end

			// Emit separator (suppressed if at end of data or before terminator)
			moreData := dataPos < len(data)
			if hasSep && moreData && (!hasTerm || r != repeatCount-1) {
				result = append(result, sep)
			}
		}

		// Emit terminator after repeat group
		if hasTerm && dataPos < len(data) {
			result = append(result, term)
		}
	}

	return string(result), true
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

// applyDisplayHintLarge handles large outputs using strings.Builder.
// strings.Builder.String() can return its internal buffer without copying
// (via unsafe), making it more efficient for larger outputs than []byte.
func applyDisplayHintLarge(hint string, data []byte, estimatedSize int) (string, bool) {
	var result strings.Builder
	result.Grow(estimatedSize)

	hintPos := 0
	dataPos := 0
	lastSpecStart := 0
	lastSpecConsumesByte := false

	for dataPos < len(data) {
		specStart := hintPos

		if hintPos >= len(hint) {
			if !lastSpecConsumesByte {
				return "", false
			}
			hintPos = lastSpecStart
			specStart = lastSpecStart
		}

		starPrefix := false
		if hintPos < len(hint) && hint[hintPos] == '*' {
			starPrefix = true
			hintPos++
		}

		if hintPos >= len(hint) || !isDigit(hint[hintPos]) {
			return "", false
		}

		take := 0
		for hintPos < len(hint) && isDigit(hint[hintPos]) {
			take = take*10 + int(hint[hintPos]-'0')
			hintPos++
		}

		if take < 0 {
			return "", false
		}

		if hintPos >= len(hint) {
			return "", false
		}

		fmtChar := hint[hintPos]
		if fmtChar != 'd' && fmtChar != 'x' && fmtChar != 'o' && fmtChar != 'a' && fmtChar != 't' {
			return "", false
		}
		hintPos++

		var sep byte
		hasSep := false
		if hintPos < len(hint) && !isDigit(hint[hintPos]) && hint[hintPos] != '*' {
			sep = hint[hintPos]
			hasSep = true
			hintPos++
		}

		var term byte
		hasTerm := false
		if starPrefix && hintPos < len(hint) && !isDigit(hint[hintPos]) && hint[hintPos] != '*' {
			term = hint[hintPos]
			hasTerm = true
			hintPos++
		}

		lastSpecStart = specStart
		lastSpecConsumesByte = (take > 0) || starPrefix

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
				// Use stack buffer with strconv.AppendUint
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

			moreData := dataPos < len(data)
			if hasSep && moreData && (!hasTerm || r != repeatCount-1) {
				result.WriteByte(sep)
			}
		}

		if hasTerm && dataPos < len(data) {
			result.WriteByte(term)
		}
	}

	return result.String(), true
}
