package oid

import (
	"strconv"
	"strings"
)

// ToList converts an OID string to a slice of integers.
func ToList(oid string) []int {
	result := make([]int, 0, strings.Count(oid, ".")+1)
	for x := range strings.SplitSeq(oid, ".") {
		o, _ := strconv.Atoi(x)
		result = append(result, o)
	}
	return result
}

// FromList converts a slice of integers to an OID string.
func FromList(l []int) string {
	if len(l) == 0 {
		return ""
	}
	var result strings.Builder
	result.Grow(len(l) * 4) // Estimate 3 digits + dot per number
	for i, o := range l {
		if i > 0 {
			result.WriteByte('.')
		}
		result.WriteString(strconv.Itoa(o))
	}
	return result.String()
}

// Split splits an OID slice at the given count.
// Right pad oid with zeros, and split at the given point.
// Some routers exclude trailing 0s in responses.
func Split(oid []int, count int) ([]int, []int) {
	head := make([]int, count)
	tailCapacity := len(oid) - count
	if tailCapacity < 0 {
		tailCapacity = 0
	}
	tail := make([]int, 0, tailCapacity)
	for i, v := range oid {
		if i < count {
			head[i] = v
		} else {
			tail = append(tail, v)
		}
	}
	return head, tail
}
