//go:build windows

// Package sessiondata contains bounded values shared by Fleet Sessions collectors,
// transport, and persistence.
package sessiondata

import "strings"

// ProcessHeap keeps only the best N session processes. It is a max-N heap
// whose root is the worst retained process, so it never accumulates the
// unbounded process list before choosing the top processes.
type ProcessHeap struct {
	limit int
	items []SessionProcess
}

// NewProcessHeap creates a fixed-capacity top-N selector. Limits outside the
// wire contract are clamped to the supported range.
func NewProcessHeap(limit int) ProcessHeap {
	if limit < 0 {
		limit = 0
	}
	if limit > 5 {
		limit = 5
	}
	return ProcessHeap{limit: limit, items: make([]SessionProcess, 0, limit)}
}

// Push considers process for the bounded top-N result.
func (h *ProcessHeap) Push(process SessionProcess) {
	if h.limit == 0 {
		return
	}
	if len(h.items) < h.limit {
		h.items = append(h.items, process)
		h.up(len(h.items) - 1)
		return
	}
	if processBetter(process, h.items[0]) {
		h.items[0] = process
		h.down(0)
	}
}

// Processes returns retained processes in wire order. It only reorders the
// fixed, at-most-five heap entries and never sorts the enumerated process set.
func (h *ProcessHeap) Processes() []SessionProcess {
	result := make([]SessionProcess, len(h.items))
	copy(result, h.items)
	for i := 1; i < len(result); i++ {
		process := result[i]
		j := i
		for j > 0 && processBetter(process, result[j-1]) {
			result[j] = result[j-1]
			j--
		}
		result[j] = process
	}
	return result
}

func (h *ProcessHeap) up(index int) {
	for index > 0 {
		parent := (index - 1) / 2
		if !processWorse(h.items[index], h.items[parent]) {
			return
		}
		h.items[index], h.items[parent] = h.items[parent], h.items[index]
		index = parent
	}
}

func (h *ProcessHeap) down(index int) {
	for {
		left := 2*index + 1
		if left >= len(h.items) {
			return
		}
		worst := left
		right := left + 1
		if right < len(h.items) && processWorse(h.items[right], h.items[left]) {
			worst = right
		}
		if !processWorse(h.items[worst], h.items[index]) {
			return
		}
		h.items[index], h.items[worst] = h.items[worst], h.items[index]
		index = worst
	}
}

func processBetter(left, right SessionProcess) bool {
	if left.CPUPercent == nil || right.CPUPercent == nil {
		if left.CPUPercent != nil {
			return true
		}
		if right.CPUPercent != nil {
			return false
		}
	} else if *left.CPUPercent != *right.CPUPercent {
		return *left.CPUPercent > *right.CPUPercent
	}
	if left.WorkingSetBytes != right.WorkingSetBytes {
		return left.WorkingSetBytes > right.WorkingSetBytes
	}
	leftName, rightName := strings.ToLower(left.ImageName), strings.ToLower(right.ImageName)
	if leftName != rightName {
		return leftName < rightName
	}
	return left.PID < right.PID
}

func processWorse(left, right SessionProcess) bool {
	return processBetter(right, left)
}
