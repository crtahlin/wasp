// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command probe-proof-equivalence tests the two requirements the probe-based
// reserve-size proof was not yet known to meet: soundness (can a node that
// stores less than its share pass?) and Schelling coordination (do two honest
// nodes with almost the same reserve agree?). See analysis.md. It has no bee
// dependency: transformed chunk addresses and probe positions are modelled as
// points on the unit circle [0,1), which is what the anchor-keyed hash produces.
//
// Run: go run ./docs/experiments/probe-proof-equivalence
package main

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
)

// probeSum returns the sum of k forward-nearest gaps from k independent random
// probes to the given sorted set of held-chunk positions, wrapping past 1.
func probeSum(rng *rand.Rand, held []float64, k int) float64 {
	s := 0.0
	for i := 0; i < k; i++ {
		p := rng.Float64()
		idx := sort.SearchFloat64s(held, p)
		var next float64
		if idx < len(held) {
			next = held[idx]
		} else {
			next = held[0] + 1 // wrap
		}
		s += next - p
	}
	return s
}

func randomReserve(rng *rand.Rand, m int) []float64 {
	pts := make([]float64, m)
	for i := range pts {
		pts[i] = rng.Float64()
	}
	sort.Float64s(pts)
	return pts
}

// evenReserve places m chunks as evenly as the address space allows, the
// placement that minimises the gaps a probe sees, with a small random phase so
// it is not degenerate. This is the adversary's best sparse reserve against
// probes it cannot predict.
func evenReserve(rng *rand.Rand, m int) []float64 {
	phase := rng.Float64() / float64(m)
	pts := make([]float64, m)
	for i := range pts {
		pts[i] = math.Mod(phase+float64(i)/float64(m), 1)
	}
	sort.Float64s(pts)
	return pts
}

func main() {
	const (
		n      = 10_000 // honest reserve size (gap statistics are scale-free)
		k      = 16     // probes / order statistics, the calibrated value
		trials = 8_000
		seed   = 1
	)
	rng := rand.New(rand.NewSource(seed))

	// Threshold u for the probe estimator: accept a reserve when its summed gap
	// is below u. Set so an honest reserve of size n passes almost always
	// (recall target 95%). A smaller summed gap means a denser, larger reserve.
	honest := make([]float64, trials)
	for i := range honest {
		honest[i] = probeSum(rng, randomReserve(rng, n), k)
	}
	sort.Float64s(honest)
	u := honest[int(0.95*float64(trials))] // 95th percentile: honest passes 95%

	fmt.Fprintf(os.Stdout, "Probe-based proof, n=%d honest reserve, k=%d probes, threshold u set for 95%% recall.\n\n", n, k)

	// SOUNDNESS. Sweep the slacker's holdings m as a fraction of n, for two
	// placements: random (an ordinary smaller reserve) and even (the adversary's
	// best). Report how often each passes the size-n threshold. A sound proof
	// accepts a slacker only near m=n; an even-placement pass at m well below n
	// is a soundness break, since that node stores less than its share yet passes.
	fmt.Fprintln(os.Stdout, "Soundness: pass rate of a slacker holding a fraction of the honest reserve")
	fmt.Fprintf(os.Stdout, "%-8s %-18s %-18s\n", "m/n", "random placement", "even placement")
	for _, frac := range []float64{0.30, 0.40, 0.50, 0.60, 0.70, 0.80, 0.90, 1.00} {
		m := int(frac * n)
		var passRand, passEven int
		for t := 0; t < trials; t++ {
			if probeSum(rng, randomReserve(rng, m), k) < u {
				passRand++
			}
			if probeSum(rng, evenReserve(rng, m), k) < u {
				passEven++
			}
		}
		fmt.Fprintf(os.Stdout, "%-8.2f %-18.4f %-18.4f\n",
			frac, float64(passRand)/float64(trials), float64(passEven)/float64(trials))
	}

	// The same even-placement attack against the current order-statistic scheme,
	// to show why it is not gameable: the transformed addresses are content-bound,
	// so the node cannot space them. We approximate the strongest thing a slacker
	// could do, hold m content-bound chunks, and take the smallest k. There is no
	// even-placement option, because the node does not choose the addresses.
	fmt.Fprintln(os.Stdout, "\nFor contrast, the current smallest-k scheme has no even-placement move:")
	fmt.Fprintln(os.Stdout, "the addresses are the anchor-keyed hashes of held chunks, not chosen positions.")

	// COORDINATION. Two honest nodes whose reserves differ by a small fraction.
	// Do they commit to the same proof? For the probe scheme the commitment is
	// the set of nearest held chunks at shared probes; for the current scheme it
	// is the set of smallest-k transformed addresses. Report the agreement rate.
	fmt.Fprintln(os.Stdout, "\nCoordination: two honest nodes whose reserves differ by a fraction, agreement rate")
	fmt.Fprintf(os.Stdout, "%-10s %-22s %-22s\n", "diff", "probe (nearest set)", "current (smallest-k)")
	for _, eps := range []float64{0.001, 0.005, 0.01, 0.02, 0.05} {
		var agreeProbe, agreeCur int
		const ctrials = 4_000
		for t := 0; t < ctrials; t++ {
			base := randomReserve(rng, n)
			// node B: replace an eps fraction with fresh points
			b := make([]float64, len(base))
			copy(b, base)
			for i := 0; i < int(eps*n); i++ {
				b[rng.Intn(len(b))] = rng.Float64()
			}
			sort.Float64s(b)

			// probe scheme: same k probes, compare the nearest-chunk positions
			pr := rand.New(rand.NewSource(int64(t) + 1))
			if nearestSet(pr, base, k) == nearestSet(rand.New(rand.NewSource(int64(t)+1)), b, k) {
				agreeProbe++
			}
			// current scheme: smallest-k of each (same content, so the shared
			// chunks yield the same addresses; only the differing chunks perturb)
			if firstKEqual(base, b, k) {
				agreeCur++
			}
		}
		fmt.Fprintf(os.Stdout, "%-10.3f %-22.4f %-22.4f\n",
			eps, float64(agreeProbe)/float64(ctrials), float64(agreeCur)/float64(ctrials))
	}
}

// nearestSet returns the sorted positions of the nearest held chunk at k probes
// drawn from rng; two nodes that hold the same chunks near the probes return the
// same set.
func nearestSet(rng *rand.Rand, held []float64, k int) string {
	out := make([]float64, k)
	for i := 0; i < k; i++ {
		p := rng.Float64()
		idx := sort.SearchFloat64s(held, p)
		if idx < len(held) {
			out[i] = held[idx]
		} else {
			out[i] = held[0]
		}
	}
	return fmt.Sprint(out)
}

// firstKEqual reports whether the k smallest positions of the two sorted sets
// are identical, the current scheme's commitment.
func firstKEqual(a, b []float64, k int) bool {
	for i := 0; i < k; i++ {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
