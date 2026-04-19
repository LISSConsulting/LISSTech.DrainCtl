//go:build windows

package evtspike

import (
	"math"
	"time"
)

const (
	slotsPerDay = 96
	minGlobalN  = 20
	confirmM    = 3
	confirmN    = 2
	// maxNBinIter bounds the negative-binomial CDF iteration. Raised from
	// 50k after codex review 2026-04-19 flagged silent miss on extreme floods
	// — at 50k the partial cdf still carried meaningful residual mass and
	// `1 - cdf` over-estimated the true tail. 10 M iterations are ~0.5 s
	// worst-case and the inner-loop underflow short-circuit means the
	// typical cost stays microseconds. See plan Phase C3.
	maxNBinIter = 10_000_000
	// negBinUnderflow is the PMF threshold below which we stop iterating.
	// When pmf falls below this the geometric-decaying remainder is
	// negligible for any alert-threshold comparison (thresholds are at
	// least 1e-12 in production).
	negBinUnderflow                 = 1e-300
	robustCapProb                   = 0.99
	defaultSlotMaturityObservations = 7
)

// GammaState holds the sufficient statistics of a Gamma(alpha, beta) posterior.
type GammaState struct {
	Alpha float64 `json:"a"`
	Beta  float64 `json:"b"`
	N     int     `json:"n"`
}

// DetectorConfig holds tuning parameters.
type DetectorConfig struct {
	MinCount                 int
	Threshold                float64
	Rho                      float64
	Cooldown                 time.Duration
	SlotMaturityObservations int
}

// Detector is a Gamma-Poisson Bayesian spike detector with time-of-day
// baselines, 2-of-3 confirmation, and robust capped updates.
type Detector struct {
	Slots       [slotsPerDay]GammaState
	Global      GammaState
	RecentFlags uint8
	LastAlert   time.Time
	Cfg         DetectorConfig
}

// Result is what ObserveBucket returns.
type Result struct {
	Count     int
	Mean      float64
	TailProb  float64
	Anomalous bool
	Alert     bool
}

// NewDetector creates a detector with a weakly informative prior.
// meanPerBucket: expected events per 10-second bucket under normal conditions.
// priorStrength: how many bucket-equivalents the prior is worth.
// halfLifeBuckets: exponential forgetting half-life in buckets.
func NewDetector(meanPerBucket float64, priorStrength float64, halfLifeBuckets float64, cfg DetectorConfig) *Detector {
	alpha := meanPerBucket * priorStrength
	if alpha < 0.1 {
		alpha = 0.1
	}
	beta := priorStrength

	rho := 1.0
	if halfLifeBuckets > 0 {
		rho = math.Exp(-math.Ln2 / halfLifeBuckets)
	}
	cfg.Rho = rho

	if cfg.SlotMaturityObservations <= 0 {
		cfg.SlotMaturityObservations = defaultSlotMaturityObservations
	}

	d := &Detector{Cfg: cfg}
	d.Global = GammaState{Alpha: alpha, Beta: beta}
	for i := range d.Slots {
		d.Slots[i] = GammaState{Alpha: alpha, Beta: beta}
	}
	return d
}

// ObserveBucket scores a 10-second bucket count and updates the baseline.
func (d *Detector) ObserveBucket(now time.Time, count int) Result {
	slot := timeSlot(now)
	s := &d.Slots[slot]

	scoring := s
	if s.N < d.Cfg.SlotMaturityObservations {
		if d.Global.N >= minGlobalN {
			scoring = &d.Global
		}
	}

	mean := scoring.Alpha / scoring.Beta
	tail := negBinUpperTail(count, scoring.Alpha, scoring.Beta)

	anomalous := count >= d.Cfg.MinCount && tail < d.Cfg.Threshold

	d.pushFlag(anomalous)
	alert := d.confirmed() && now.Sub(d.LastAlert) >= d.Cfg.Cooldown
	if alert {
		d.LastAlert = now
	}

	// Robust cap: during a confirmed anomaly, clamp the update to the 99th
	// percentile of the current posterior so a sustained flood cannot poison
	// the baseline and mask a follow-up anomaly (SC-004 / US2 Independent
	// Test). If the quantile iteration overflows (extreme flood beyond our
	// float precision), skip the baseline update entirely — updating with
	// a saturated sentinel would re-introduce the poisoning path the cap
	// exists to prevent (plan C3).
	updateY := float64(count)
	skipBaseline := false
	if anomalous {
		capY, ok := negBinQuantile(robustCapProb, scoring.Alpha, scoring.Beta)
		if !ok {
			skipBaseline = true
		} else if updateY > float64(capY) {
			updateY = float64(capY)
		}
	}

	if !skipBaseline {
		ewmaUpdate(s, updateY, d.Cfg.Rho)
		ewmaUpdate(&d.Global, updateY, d.Cfg.Rho)
	}

	return Result{
		Count:     count,
		Mean:      mean,
		TailProb:  tail,
		Anomalous: anomalous,
		Alert:     alert,
	}
}

func ewmaUpdate(s *GammaState, y float64, rho float64) {
	s.Alpha = rho*s.Alpha + y
	s.Beta = rho*s.Beta + 1.0
	s.N++
}

func timeSlot(t time.Time) int {
	return (t.Hour()*60 + t.Minute()) / 15
}

func (d *Detector) pushFlag(anomalous bool) {
	d.RecentFlags <<= 1
	if anomalous {
		d.RecentFlags |= 1
	}
	d.RecentFlags &= (1 << confirmM) - 1
}

func (d *Detector) confirmed() bool {
	flags := d.RecentFlags
	n := 0
	for i := 0; i < confirmM; i++ {
		if flags&1 == 1 {
			n++
		}
		flags >>= 1
	}
	return n >= confirmN
}

// negBinUpperTail returns P(Y >= y) where Y ~ NegBin(r=alpha, p=beta/(beta+1)).
func negBinUpperTail(y int, alpha, beta float64) float64 {
	if y <= 0 {
		return 1.0
	}
	p := beta / (beta + 1.0)
	q := 1.0 - p

	logPMF0 := alpha * math.Log(p)
	if logPMF0 < -700 {
		return 0.0
	}
	pmf := math.Exp(logPMF0)
	cdf := pmf

	for k := 0; k < y-1 && k < maxNBinIter; k++ {
		pmf *= ((float64(k) + alpha) / float64(k+1)) * q
		cdf += pmf
		if cdf >= 1.0 {
			return 0.0
		}
		if pmf < negBinUnderflow {
			// Remaining mass is bounded by a geometric tail with ratio
			// approaching q — negligible compared to any alert threshold.
			// Stop iterating; treat remaining tail as zero.
			break
		}
	}
	tail := 1.0 - cdf
	if tail < 0 {
		return 0
	}
	return tail
}

// negBinQuantile returns the smallest y such that P(Y <= y) >= prob and a
// boolean `ok` that is false iff the iteration hit maxNBinIter without
// reaching prob (an extreme-flood signal). Callers MUST branch on ok:
// returning a best-effort y on overflow would silently disable downstream
// flood-poisoning protection (FR-009 / plan C3).
func negBinQuantile(prob float64, alpha, beta float64) (int, bool) {
	if prob <= 0 {
		return 0, true
	}
	p := beta / (beta + 1.0)
	q := 1.0 - p

	logPMF0 := alpha * math.Log(p)
	if logPMF0 < -700 {
		return 0, true
	}
	pmf := math.Exp(logPMF0)
	cdf := pmf
	if cdf >= prob {
		return 0, true
	}
	for k := 0; k < maxNBinIter; k++ {
		pmf *= ((float64(k) + alpha) / float64(k+1)) * q
		cdf += pmf
		if cdf >= prob {
			return k + 1, true
		}
		if pmf == 0 {
			// PMF underflowed to zero before reaching prob — the true
			// quantile is beyond our floating-point precision; signal
			// overflow so the caller refuses to update the baseline.
			return maxNBinIter, false
		}
	}
	return maxNBinIter, false
}
