// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command probe-sample-k estimates how many probes k the probe-based
// reserve-size estimator needs to match the accuracy of the current
// order-statistic estimator, for issue #245. See analysis.md.
//
// Both estimators are Erlang(shape k, rate proportional to reserve size):
//   - current: x_k, the k-th smallest of N uniform transformed addresses, is the
//     sum of k inter-point gaps, each ~Exp(N+1), so x_k ~ Erlang(k, N+1) (Swarm
//     "Future-proof Storage" spec, Appendix C).
//   - probe: the sum of k nearest-neighbour gaps, each ~Exp(N) (forward gap in a
//     rate-N point process, matching the forward-successor probe the ProbeSample
//     benchmark uses), so S ~ Erlang(k, N).
//
// The recall/precision (alpha, beta) for distinguishing an honest reserve n from a
// slacker at m > n therefore depends only on k and the ratio m/n, not on the rate
// constant, so the two estimators need the same k. This program computes alpha and
// beta in closed form for both, reproduces the spec's Table 3 for the current
// estimator as a validation anchor, and confirms the probe estimator by a direct
// Monte Carlo of the nearest-neighbour gaps.
//
// Run: go run ./docs/experiments/probe-sample-k
package main

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
)

// erlangCDF returns P(X <= x) for X ~ Erlang(shape k, rate lambda), using the
// finite-sum closed form valid for integer k:
//
//	F(x) = 1 - e^{-lambda x} * sum_{i=0}^{k-1} (lambda x)^i / i!
func erlangCDF(x float64, k int, lambda float64) float64 {
	if x <= 0 {
		return 0
	}
	lx := lambda * x
	sum := 0.0
	term := 1.0 // (lx)^0 / 0!
	for i := 0; i < k; i++ {
		if i > 0 {
			term *= lx / float64(i)
		}
		sum += term
	}
	return 1 - math.Exp(-lx)*sum
}

// bestAlphaBeta scans the threshold u and returns the alpha, beta and u that
// minimise alpha+beta, where the test accepts a reserve when its estimator value
// is below u. alpha is a small reserve n passing (false accept of a slacker),
// beta is a large reserve m failing (false reject of an honest node).
//
//	alpha(u) = P(estimator < u | reserve n) = erlangCDF(u, k, lambdaN)
//	beta(u)  = P(estimator > u | reserve m) = 1 - erlangCDF(u, k, lambdaM)
func bestAlphaBeta(k int, lambdaN, lambdaM float64) (alpha, beta, u float64) {
	// The estimator's mass sits near k/lambda; scan a generous range around it.
	lo, hi := 0.0, 4*float64(k)/lambdaM+4*float64(k)/lambdaN
	best := math.Inf(1)
	const steps = 200000
	for i := 0; i <= steps; i++ {
		uu := lo + (hi-lo)*float64(i)/float64(steps)
		a := erlangCDF(uu, k, lambdaN)
		b := 1 - erlangCDF(uu, k, lambdaM)
		if a+b < best {
			best, alpha, beta, u = a+b, a, b, uu
		}
	}
	return
}

func main() {
	const (
		n = 1_000_000.0 // honest reserve size (spec Appendix C scale)
		m = 2_000_000.0 // slacker discrimination target, m = 2n
	)
	ks := []int{8, 16, 32, 64, 128}

	fmt.Fprintln(os.Stdout, "Analytic alpha/beta for distinguishing n=1e6 from m=2e6 (minimising alpha+beta)")
	fmt.Fprintln(os.Stdout)
	fmt.Fprintf(os.Stdout, "%-5s | %-28s | %-28s\n", "k", "current: x_k ~ Erlang(k,N+1)", "probe: sum gaps ~ Erlang(k,N)")
	fmt.Fprintf(os.Stdout, "%-5s | %-9s %-9s %-8s | %-9s %-9s %-8s\n", "", "alpha", "beta", "a+b", "alpha", "beta", "a+b")
	for _, k := range ks {
		ca, cb, _ := bestAlphaBeta(k, n+1, m+1) // current: rate N+1
		pa, pb, _ := bestAlphaBeta(k, n, m)     // probe: rate N (forward gap)
		fmt.Fprintf(os.Stdout, "%-5d | %-9.4f %-9.4f %-8.4f | %-9.4f %-9.4f %-8.4f\n", k, ca, cb, ca+cb, pa, pb, pa+pb)
	}
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "Validation anchor: spec Table 3 gives the current estimator at k=16 alpha~0.098, beta~0.072.")

	// Monte Carlo: confirm the probe estimator (sum of k forward-nearest gaps over
	// N uniform points) really is Erlang(k, N), by measuring its empirical alpha
	// and beta and comparing to the closed form. Uses a smaller N for speed; the
	// shape does not depend on N.
	fmt.Fprintln(os.Stdout)
	const (
		mcN      = 20_000
		mcM      = 40_000
		trials   = 4_000
		mcK      = 16
		seed     = 1
		unitSpan = 1.0
	)
	fmt.Fprintf(os.Stdout, "Monte Carlo check of the probe estimator (empirical vs analytic), N=%d vs %d, %d trials\n", mcN, mcM, trials)
	rng := rand.New(rand.NewSource(seed))
	// threshold from the analytic optimum for this N/M
	_, _, u := bestAlphaBeta(mcK, mcN, mcM)
	probeSum := func(count int) float64 { // sum of mcK forward-nearest gaps over `count` uniform points
		pts := make([]float64, count)
		for i := range pts {
			pts[i] = rng.Float64() * unitSpan
		}
		sort.Float64s(pts)
		s := 0.0
		for j := 0; j < mcK; j++ {
			probe := rng.Float64() * unitSpan
			// forward nearest: first point >= probe (wrap to the first point)
			idx := sort.SearchFloat64s(pts, probe)
			var nxt float64
			if idx < len(pts) {
				nxt = pts[idx]
			} else {
				nxt = pts[0] + unitSpan
			}
			s += nxt - probe
		}
		return s
	}
	var falseAccept, falseReject int
	for t := 0; t < trials; t++ {
		if probeSum(mcN) < u { // reserve n: accepted -> false accept of a slacker at n vs the m target
			falseAccept++
		}
		if probeSum(mcM) >= u { // reserve m: rejected -> false reject of the honest larger reserve
			falseReject++
		}
	}
	empA := float64(falseAccept) / float64(trials)
	empB := float64(falseReject) / float64(trials)
	anA, anB, _ := bestAlphaBeta(mcK, mcN, mcM)
	fmt.Fprintf(os.Stdout, "k=%d: empirical alpha=%.4f beta=%.4f   analytic alpha=%.4f beta=%.4f\n", mcK, empA, empB, anA, anB)
}
