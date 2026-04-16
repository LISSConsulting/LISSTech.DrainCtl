//go:build windows

package main

import (
	"math"
	"time"
)

const (
	slotsPerDay   = 96 // 15-minute slots
	minSlotN      = 5  // minimum observations before trusting a slot
	minGlobalN    = 20
	confirmM      = 3
	confirmN      = 2
	maxNBinIter   = 50000 // cap NegBin recurrence loops
	robustCapProb = 0.99
)

// GammaState holds the sufficient statistics of a Gamma(alpha, beta) posterior.
type GammaState struct {
	Alpha float64
	Beta  float64
	N     int
}

// DetectorConfig holds tuning parameters.
type DetectorConfig struct {
	MinCount  int           // absolute floor — don't alert below this
	Threshold float64       // tail probability threshold (e.g. 1e-4)
	Rho       float64       // forgetting factor per bucket
	Cooldown  time.Duration // suppress repeat alerts
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

	// Pick scoring state: slot if mature, else global.
	scoring := s
	if s.N < minSlotN {
		if d.Global.N >= minGlobalN {
			scoring = &d.Global
		}
		// else score with immature slot (wide prior → conservative)
	}

	mean := scoring.Alpha / scoring.Beta
	tail := negBinUpperTail(count, scoring.Alpha, scoring.Beta)

	anomalous := count >= d.Cfg.MinCount && tail < d.Cfg.Threshold

	d.pushFlag(anomalous)
	alert := d.confirmed() && now.Sub(d.LastAlert) >= d.Cfg.Cooldown
	if alert {
		d.LastAlert = now
	}

	// Robust update: cap y during anomalies to avoid poisoning baseline.
	updateY := float64(count)
	if anomalous {
		cap := float64(negBinQuantile(robustCapProb, scoring.Alpha, scoring.Beta))
		if updateY > cap {
			updateY = cap
		}
	}

	// Discounted posterior update — both slot and global.
	ewmaUpdate(s, updateY, d.Cfg.Rho)
	ewmaUpdate(&d.Global, updateY, d.Cfg.Rho)

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
	if logPMF0 < -700 { // underflow guard
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
	}
	tail := 1.0 - cdf
	if tail < 0 {
		return 0
	}
	return tail
}

// negBinQuantile returns the smallest y such that P(Y <= y) >= prob.
func negBinQuantile(prob float64, alpha, beta float64) int {
	if prob <= 0 {
		return 0
	}
	p := beta / (beta + 1.0)
	q := 1.0 - p

	logPMF0 := alpha * math.Log(p)
	if logPMF0 < -700 {
		return 0
	}
	pmf := math.Exp(logPMF0)
	cdf := pmf
	if cdf >= prob {
		return 0
	}
	for k := 0; k < maxNBinIter; k++ {
		pmf *= ((float64(k) + alpha) / float64(k+1)) * q
		cdf += pmf
		if cdf >= prob || pmf == 0 {
			return k + 1
		}
	}
	return maxNBinIter
}
