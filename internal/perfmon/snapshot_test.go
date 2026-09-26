//go:build windows

package perfmon

import (
	"math"
	"testing"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

func TestPercentile_Empty(t *testing.T) {
	if got := Percentile(nil, 50); got != 0 {
		t.Errorf("Percentile(nil, 50) = %v, want 0", got)
	}
}

func TestPercentile_SingleValue(t *testing.T) {
	if got := Percentile([]float64{42}, 95); got != 42 {
		t.Errorf("Percentile([42], 95) = %v, want 42", got)
	}
}

func TestPercentile_KnownValues(t *testing.T) {
	data := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	tests := []struct {
		p    float64
		want float64
	}{
		{0, 1},
		{50, 5.5},
		{95, 9.55},
		{100, 10},
	}
	for _, tt := range tests {
		got := Percentile(data, tt.p)
		if math.Abs(got-tt.want) > 0.01 {
			t.Errorf("Percentile(1..10, %.0f) = %v, want %v", tt.p, got, tt.want)
		}
	}
}

func TestAggregateValues_Empty(t *testing.T) {
	p50, p95, max := AggregateValues(nil)
	if p50 != 0 || p95 != 0 || max != 0 {
		t.Errorf("AggregateValues(nil) = (%v, %v, %v), want (0, 0, 0)", p50, p95, max)
	}
}

func TestAggregateValues_Sorted(t *testing.T) {
	values := []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	p50, p95, max := AggregateValues(values)
	if math.Abs(p50-55) > 0.01 {
		t.Errorf("p50 = %v, want ~55", p50)
	}
	if math.Abs(p95-95.5) > 0.01 {
		t.Errorf("p95 = %v, want ~95.5", p95)
	}
	if max != 100 {
		t.Errorf("max = %v, want 100", max)
	}
}

func TestAggregateValues_Unsorted(t *testing.T) {
	values := []float64{50, 10, 90, 30, 70}
	p50, _, max := AggregateValues(values)
	if p50 != 50 {
		t.Errorf("p50 = %v, want 50", p50)
	}
	if max != 90 {
		t.Errorf("max = %v, want 90", max)
	}
}

func TestAggregateServicePercentiles_Direction(t *testing.T) {
	higher := []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	p50, serviceP95 := AggregateServicePercentiles(higher, true)
	if math.Abs(p50-55) > 0.01 || math.Abs(serviceP95-14.5) > 0.01 {
		t.Errorf("higher-is-better = (P50 %v, service P95 %v), want (55, 14.5)", p50, serviceP95)
	}

	lower := []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	p50, serviceP95 = AggregateServicePercentiles(lower, false)
	if math.Abs(p50-55) > 0.01 || math.Abs(serviceP95-95.5) > 0.01 {
		t.Errorf("lower-is-better = (P50 %v, P95 %v), want (55, 95.5)", p50, serviceP95)
	}
}

func TestFilterRemoteFXValues_ValidationAndDirectionalPercentiles(t *testing.T) {
	tests := []struct {
		name           string
		values         []float64
		min, max       float64
		higherIsBetter bool
		want           []float64
		wantP50        float64
		wantServiceP95 float64
	}{
		{
			name:           "fps omits inactive and invalid instances",
			values:         []float64{0, 30, 60, -1, 241, math.NaN(), math.Inf(1)},
			min:            0,
			max:            240,
			higherIsBetter: true,
			want:           []float64{30, 60},
			wantP50:        45,
			wantServiceP95: 31.5,
		},
		{
			name:           "quality accepts its active range",
			values:         []float64{0, 50, 100, 101, math.Inf(-1)},
			min:            0,
			max:            100,
			higherIsBetter: true,
			want:           []float64{50, 100},
			wantP50:        75,
			wantServiceP95: 52.5,
		},
		{
			name:           "loss retains meaningful zeroes",
			values:         []float64{0, 1, -1, 101, math.NaN(), math.Inf(1)},
			min:            0,
			max:            100,
			want:           []float64{0, 1},
			wantP50:        0.5,
			wantServiceP95: 0.95,
		},
		{
			name:           "rtt accepts bounded zero and maximum",
			values:         []float64{0, 60000, 60001, math.Inf(1)},
			min:            0,
			max:            60000,
			want:           []float64{0, 60000},
			wantP50:        30000,
			wantServiceP95: 57000,
		},
		{
			name:           "skipped frames rejects sentinel outliers",
			values:         []float64{0, 1000000, 1000001, math.NaN()},
			min:            0,
			max:            1000000,
			want:           []float64{0, 1000000},
			wantP50:        500000,
			wantServiceP95: 950000,
		},
		{
			name:           "inactive higher-is-better instances leave no percentile",
			values:         []float64{0, -1, 241, math.NaN(), math.Inf(1)},
			min:            0,
			max:            240,
			higherIsBetter: true,
			want:           []float64{},
			wantP50:        0,
			wantServiceP95: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := append([]float64(nil), tt.values...)
			filtered := filterRemoteFXValues(values, tt.min, tt.max, tt.higherIsBetter)
			if len(filtered) != len(tt.want) {
				t.Fatalf("len(filtered) = %d, want %d", len(filtered), len(tt.want))
			}
			for i, want := range tt.want {
				if filtered[i] != want {
					t.Errorf("filtered[%d] = %v, want %v", i, filtered[i], want)
				}
			}
			if len(filtered) > 0 && &filtered[0] != &values[0] {
				t.Error("filtered values must reuse the PDH values backing array")
			}
			p50, serviceP95 := AggregateServicePercentiles(filtered, tt.higherIsBetter)
			if p50 != tt.wantP50 || serviceP95 != tt.wantServiceP95 {
				t.Errorf("percentiles = (%v, %v), want (%v, %v)",
					p50, serviceP95, tt.wantP50, tt.wantServiceP95)
			}
		})
	}
}

func TestAggregate_PreservesSessionPercentiles(t *testing.T) {
	got := aggregate([]dc.PerfSnapshot{
		{SessionCPUP50: 2.1, SessionCPUP95: 8.4, SessionMemP50: 100, SessionMemP95: 400},
		{SessionCPUP50: 3.2, SessionCPUP95: 7.5, SessionMemP50: 150, SessionMemP95: 350},
	})

	if got.SessionCPUP50 != 3.2 || got.SessionCPUP95 != 8.4 {
		t.Errorf("session CPU percentiles = (P50 %v, P95 %v), want (P50 3.2, P95 8.4)",
			got.SessionCPUP50, got.SessionCPUP95)
	}
	if got.SessionMemP50 != 150 || got.SessionMemP95 != 400 {
		t.Errorf("session memory percentiles = (P50 %v, P95 %v), want (P50 150, P95 400)",
			got.SessionMemP50, got.SessionMemP95)
	}
}

func TestAggregate_PreservesDirectionalRemoteFXPercentiles(t *testing.T) {
	got := aggregate([]dc.PerfSnapshot{
		{
			RFXAvailable: true, RFXFPSOut: 20, RFXFPSOutP50: 30, RFXQuality: 70, RFXQualityP50: 80,
			RFXEncodeMS: 8, RFXEncodeMSP50: 4, RFXRTT: 40, RFXRTTP50: 20,
		},
		{
			RFXAvailable: true, RFXFPSOut: 15, RFXFPSOutP50: 25, RFXQuality: 60, RFXQualityP50: 75,
			RFXEncodeMS: 10, RFXEncodeMSP50: 5, RFXRTT: 35, RFXRTTP50: 18,
		},
	})
	if got.RFXFPSOut != 15 || got.RFXFPSOutP50 != 25 || got.RFXQuality != 60 || got.RFXQualityP50 != 75 {
		t.Errorf("higher-is-better temporal floor = FPS (%v,%v) quality (%v,%v), want (15,25) (60,75)",
			got.RFXFPSOut, got.RFXFPSOutP50, got.RFXQuality, got.RFXQualityP50)
	}
	if got.RFXEncodeMS != 10 || got.RFXEncodeMSP50 != 5 || got.RFXRTT != 40 || got.RFXRTTP50 != 20 {
		t.Errorf("lower-is-better temporal ceiling = encode (%v,%v) RTT (%v,%v), want (10,5) (40,20)",
			got.RFXEncodeMS, got.RFXEncodeMSP50, got.RFXRTT, got.RFXRTTP50)
	}
}

func TestSanitizePerfFieldRejectsInvalidValues(t *testing.T) {
	for _, test := range []struct {
		field string
		value float64
	}{
		{"cpu_pct", math.NaN()},
		{"rfx_rtt_ms", math.Inf(1)},
		{"rfx_quality_pct", 101},
		{"session_mem_p50_bytes", -1},
	} {
		if _, ok := dc.SanitizePerfField(test.field, test.value); ok {
			t.Errorf("SanitizePerfField(%q, %v) accepted invalid value", test.field, test.value)
		}
	}
	if got, ok := dc.SanitizePerfField("rfx_loss_pct_p50", 0); !ok || got != 0 {
		t.Errorf("SanitizePerfField valid zero = (%v, %v), want (0, true)", got, ok)
	}
}

func TestSanitizePerfSnapshotClearsInvalidP50Presence(t *testing.T) {
	got := dc.SanitizePerfSnapshot(dc.PerfSnapshot{
		RFXLossP50: 101,
		P50Present: dc.PerfP50RFXLoss,
	})
	if got.RFXLossP50 != 0 || got.HasP50(dc.PerfP50RFXLoss, got.RFXLossP50) {
		t.Errorf("invalid P50 = (%v, present=%v), want (0, false)",
			got.RFXLossP50, got.HasP50(dc.PerfP50RFXLoss, got.RFXLossP50))
	}
}

func TestAggregateRetainsPresentLowerIsBetterZeroP50(t *testing.T) {
	got := aggregate([]dc.PerfSnapshot{
		{RFXAvailable: true, RFXLossP50: 0, P50Present: dc.PerfP50RFXLoss},
		{RFXAvailable: true, RFXLossP50: 5, P50Present: dc.PerfP50RFXLoss},
	})
	if got.RFXLossP50 != 5 || !got.HasP50(dc.PerfP50RFXLoss, got.RFXLossP50) {
		t.Errorf("loss P50 = (%v, present=%v), want (5, true)",
			got.RFXLossP50, got.HasP50(dc.PerfP50RFXLoss, got.RFXLossP50))
	}
}

func TestAggregateIgnoresInactiveHighIsBetterZeros(t *testing.T) {
	got := aggregate([]dc.PerfSnapshot{
		{RFXAvailable: true},
		{
			RFXAvailable:  true,
			RFXFPSOut:     15,
			RFXFPSOutP50:  20,
			RFXQuality:    70,
			RFXQualityP50: 80,
			P50Present: dc.PerfP50RFXFPSOut |
				dc.PerfP50RFXQuality,
		},
		{
			RFXAvailable:  true,
			RFXFPSOut:     18,
			RFXFPSOutP50:  24,
			RFXQuality:    75,
			RFXQualityP50: 85,
			P50Present: dc.PerfP50RFXFPSOut |
				dc.PerfP50RFXQuality,
		},
	})
	if got.RFXFPSOut != 15 || got.RFXFPSOutP50 != 20 || got.RFXQuality != 70 || got.RFXQualityP50 != 80 {
		t.Errorf("high-is-better temporal values = FPS (%v,%v), quality (%v,%v), want (15,20) (70,80)",
			got.RFXFPSOut, got.RFXFPSOutP50, got.RFXQuality, got.RFXQualityP50)
	}
}

func TestRoundTo(t *testing.T) {
	tests := []struct {
		val    float64
		places int
		want   float64
	}{
		{3.14159, 2, 3.14},
		{3.14159, 0, 3},
		{99.999, 1, 100},
		{0, 5, 0},
	}
	for _, tt := range tests {
		got := RoundTo(tt.val, tt.places)
		if got != tt.want {
			t.Errorf("RoundTo(%v, %d) = %v, want %v", tt.val, tt.places, got, tt.want)
		}
	}
}
