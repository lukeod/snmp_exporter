package collector

import (
	"strconv"
	"strings"
	"testing"
)

// Original implementations for comparison (renamed)
func oidToListOriginal(oid string) []int {
	result := []int{}
	for x := range strings.SplitSeq(oid, ".") {
		o, _ := strconv.Atoi(x)
		result = append(result, o)
	}
	return result
}

func listToOidOriginal(l []int) string {
	var result []string
	for _, o := range l {
		result = append(result, strconv.Itoa(o))
	}
	return strings.Join(result, ".")
}

func splitOidOriginal(oid []int, count int) ([]int, []int) {
	head := make([]int, count)
	tail := []int{}
	for i, v := range oid {
		if i < count {
			head[i] = v
		} else {
			tail = append(tail, v)
		}
	}
	return head, tail
}

// Benchmarks
func BenchmarkOidToList(b *testing.B) {
	oid := "1.3.6.1.2.1.2.2.1.2.10101"
	b.Run("Original", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			oidToListOriginal(oid)
		}
	})
	b.Run("Current", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			oidToList(oid)
		}
	})
}

func BenchmarkListToOid(b *testing.B) {
	list := []int{1, 3, 6, 1, 2, 1, 2, 2, 1, 2, 10101}
	b.Run("Original", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			listToOidOriginal(list)
		}
	})
	b.Run("Current", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			listToOid(list)
		}
	})
}

func BenchmarkSplitOid(b *testing.B) {
	list := []int{1, 3, 6, 1, 2, 1, 2, 2, 1, 2, 10101}
	count := 6
	b.Run("Original", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			splitOidOriginal(list, count)
		}
	})
	b.Run("Current", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			splitOid(list, count)
		}
	})
}
