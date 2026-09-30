//go:build windows

package sessiondata

import (
	"encoding/binary"
	"errors"
	"math"
	"slices"
)

const (
	CPUHistogramBins = 201
	cpuBinWidth      = 0.5
	memoryBinRatio   = 1.02
	workloadCodecV1  = 1
)

var ErrInvalidWorkloadHistogram = errors.New("sessiondata: invalid workload histogram")

// SessionWorkloadAggregate is an anonymous sufficient statistic for one host
// snapshot. It deliberately contains no session identity or per-session rows.
type SessionWorkloadAggregate struct {
	BaseSampleCount      uint64
	SuccessfulEmptyCount uint64
	ErrorSampleCount     uint64
	CPUSum               float64
	CPUCount             uint64
	CPUHistogram         [CPUHistogramBins]uint64
	CPUGE5               uint64
	CPUGE20              uint64
	MemorySumBytes       float64
	MemoryCount          uint64
	MemoryZeroCount      uint64
	MemoryHistogram      map[int32]uint64
}

// CalculateSessionWorkload derives anonymous metrics from a validated snapshot.
// A fatal snapshot produces error coverage and no observations.
func CalculateSessionWorkload(snapshot SessionSnapshot) SessionWorkloadAggregate {
	if snapshot.CollectionError != nil {
		return SessionWorkloadAggregate{ErrorSampleCount: 1}
	}
	result := SessionWorkloadAggregate{BaseSampleCount: 1}
	if len(snapshot.Sessions) == 0 {
		result.SuccessfulEmptyCount = 1
	}
	for _, session := range snapshot.Sessions {
		if !workloadEligibleState(session.State) {
			continue
		}
		if session.CPUPercent != nil && snapshot.LogicalCPUCount > 0 {
			value := *session.CPUPercent / float64(snapshot.LogicalCPUCount)
			if !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 100 {
				result.CPUSum += value
				result.CPUCount++
				bin := int(math.Round(value / cpuBinWidth))
				if bin < 0 {
					bin = 0
				} else if bin >= CPUHistogramBins {
					bin = CPUHistogramBins - 1
				}
				result.CPUHistogram[bin]++
				if value >= 5 {
					result.CPUGE5++
				}
				if value >= 20 {
					result.CPUGE20++
				}
			}
		}
		if session.WorkingSetBytes != nil {
			value := session.WorkingSetBytes.Uint64()
			result.MemorySumBytes += float64(value)
			result.MemoryCount++
			if value == 0 {
				result.MemoryZeroCount++
			} else {
				if result.MemoryHistogram == nil {
					result.MemoryHistogram = make(map[int32]uint64)
				}
				result.MemoryHistogram[memoryBin(value)]++
			}
		}
	}
	return result
}

func workloadEligibleState(state SessionState) bool {
	switch state {
	case SessionActive, SessionConnected, SessionConnectQuery, SessionShadow, SessionDisconnected, SessionIdle:
		return true
	default:
		return false
	}
}

func memoryBin(value uint64) int32 {
	return int32(math.Floor(math.Log(float64(value)) / math.Log(memoryBinRatio)))
}

func memoryBinValue(bin int32) float64 {
	return math.Pow(memoryBinRatio, float64(bin)+0.5)
}

// Merge adds another aggregate without changing statistical meaning.
func (a *SessionWorkloadAggregate) Merge(other SessionWorkloadAggregate) {
	a.BaseSampleCount += other.BaseSampleCount
	a.SuccessfulEmptyCount += other.SuccessfulEmptyCount
	a.ErrorSampleCount += other.ErrorSampleCount
	a.CPUSum += other.CPUSum
	a.CPUCount += other.CPUCount
	for i := range a.CPUHistogram {
		a.CPUHistogram[i] += other.CPUHistogram[i]
	}
	a.CPUGE5 += other.CPUGE5
	a.CPUGE20 += other.CPUGE20
	a.MemorySumBytes += other.MemorySumBytes
	a.MemoryCount += other.MemoryCount
	a.MemoryZeroCount += other.MemoryZeroCount
	if len(other.MemoryHistogram) > 0 && a.MemoryHistogram == nil {
		a.MemoryHistogram = make(map[int32]uint64, len(other.MemoryHistogram))
	}
	for bin, count := range other.MemoryHistogram {
		a.MemoryHistogram[bin] += count
	}
}

func (a SessionWorkloadAggregate) CPUPercentile(q float64) (float64, bool) {
	if a.CPUCount == 0 {
		return 0, false
	}
	rank := percentileRank(a.CPUCount, q)
	var seen uint64
	for i, count := range a.CPUHistogram {
		seen += count
		if seen >= rank {
			return float64(i) * cpuBinWidth, true
		}
	}
	return 0, false
}

func (a SessionWorkloadAggregate) MemoryPercentile(q float64) (float64, bool) {
	if a.MemoryCount == 0 {
		return 0, false
	}
	rank := percentileRank(a.MemoryCount, q)
	if rank <= a.MemoryZeroCount {
		return 0, true
	}
	seen := a.MemoryZeroCount
	bins := make([]int32, 0, len(a.MemoryHistogram))
	for bin := range a.MemoryHistogram {
		bins = append(bins, bin)
	}
	slices.Sort(bins)
	for _, bin := range bins {
		seen += a.MemoryHistogram[bin]
		if seen >= rank {
			return memoryBinValue(bin), true
		}
	}
	return 0, false
}

func percentileRank(count uint64, q float64) uint64 {
	if q <= 0 {
		return 1
	}
	if q >= 1 {
		return count
	}
	return uint64(math.Ceil(q * float64(count)))
}

// EncodeCPUHistogram stores aggregate bin counts only.
func (a SessionWorkloadAggregate) EncodeCPUHistogram() []byte {
	result := make([]byte, 1, 1+CPUHistogramBins)
	result[0] = workloadCodecV1
	var scratch [binary.MaxVarintLen64]byte
	for _, count := range a.CPUHistogram {
		n := binary.PutUvarint(scratch[:], count)
		result = append(result, scratch[:n]...)
	}
	return result
}

func DecodeCPUHistogram(data []byte) ([CPUHistogramBins]uint64, error) {
	var result [CPUHistogramBins]uint64
	if len(data) == 0 || data[0] != workloadCodecV1 {
		return result, ErrInvalidWorkloadHistogram
	}
	data = data[1:]
	for i := range result {
		value, n := binary.Uvarint(data)
		if n <= 0 {
			return result, ErrInvalidWorkloadHistogram
		}
		result[i] = value
		data = data[n:]
	}
	if len(data) != 0 {
		return result, ErrInvalidWorkloadHistogram
	}
	return result, nil
}

// EncodeMemoryHistogram stores zero count followed by sorted delta-index/count pairs.
func (a SessionWorkloadAggregate) EncodeMemoryHistogram() []byte {
	result := make([]byte, 1, 32)
	result[0] = workloadCodecV1
	var scratch [binary.MaxVarintLen64]byte
	appendUvarint := func(value uint64) { n := binary.PutUvarint(scratch[:], value); result = append(result, scratch[:n]...) }
	appendUvarint(a.MemoryZeroCount)
	bins := make([]int32, 0, len(a.MemoryHistogram))
	for bin := range a.MemoryHistogram {
		bins = append(bins, bin)
	}
	slices.Sort(bins)
	appendUvarint(uint64(len(bins)))
	var previous int32
	for i, bin := range bins {
		delta := bin
		if i > 0 {
			delta = bin - previous
		}
		appendUvarint(uint64(delta))
		appendUvarint(a.MemoryHistogram[bin])
		previous = bin
	}
	return result
}

func DecodeMemoryHistogram(data []byte) (uint64, map[int32]uint64, error) {
	if len(data) == 0 || data[0] != workloadCodecV1 {
		return 0, nil, ErrInvalidWorkloadHistogram
	}
	data = data[1:]
	read := func() (uint64, bool) {
		value, n := binary.Uvarint(data)
		if n <= 0 {
			return 0, false
		}
		data = data[n:]
		return value, true
	}
	zero, ok := read()
	if !ok {
		return 0, nil, ErrInvalidWorkloadHistogram
	}
	length, ok := read()
	if !ok || length > 10000 {
		return 0, nil, ErrInvalidWorkloadHistogram
	}
	result := make(map[int32]uint64, int(length))
	var bin int32
	for i := uint64(0); i < length; i++ {
		delta, ok := read()
		if !ok || delta > math.MaxInt32 {
			return 0, nil, ErrInvalidWorkloadHistogram
		}
		if i == 0 {
			bin = int32(delta)
		} else {
			bin += int32(delta)
		}
		count, ok := read()
		if !ok || count == 0 {
			return 0, nil, ErrInvalidWorkloadHistogram
		}
		result[bin] = count
	}
	if len(data) != 0 {
		return 0, nil, ErrInvalidWorkloadHistogram
	}
	return zero, result, nil
}
