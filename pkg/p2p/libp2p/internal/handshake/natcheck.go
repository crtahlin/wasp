// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handshake

import (
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// natDisagreement compares the public IPs a peer observed for this node with
// the same observations after the nat-addr resolver has rewritten them, which
// is what this node advertises for them. The node's own listen addresses are
// deliberately left out: they say nothing about nat-addr, and a host's global
// IPv6 address that a peer sees rewritten would otherwise read as a stale
// nat-addr. It reports a disagreement when, for an IP family present on both
// sides, none of the observed IPs is advertised, and returns one IP from each
// side to name in a warning.
//
// Only the same family is compared: a peer that reached the node over IPv6
// says nothing about a configured IPv4 address. With no nat-addr, or a
// port-only one, the resolver keeps the observed IP, and with a DNS name the
// advertised address carries no IP, so none of those ever disagrees. It
// exists for a nat-addr that carries an IP, which never follows a public IP
// change. See #500.
func natDisagreement(observed, advertised []ma.Multiaddr) (disagrees bool, observedIP, advertisedIP string) {
	obs := publicIPsByFamily(observed)
	adv := publicIPsByFamily(advertised)
	for family, o := range obs {
		a, ok := adv[family]
		if !ok {
			continue
		}
		if sharesKey(o, a) {
			continue
		}
		return true, anyKey(o), anyKey(a)
	}
	return false, "", ""
}

// publicIPsByFamily groups the global-scope IPs among the underlays by
// family, 4 or 6.
func publicIPsByFamily(underlays []ma.Multiaddr) map[int]map[string]struct{} {
	out := make(map[int]map[string]struct{})
	for _, a := range underlays {
		if !manet.IsPublicAddr(a) {
			continue
		}
		ip, err := manet.ToIP(a)
		if err != nil {
			continue
		}
		family := 6
		if ip.To4() != nil {
			family = 4
		}
		if out[family] == nil {
			out[family] = make(map[string]struct{})
		}
		out[family][ip.String()] = struct{}{}
	}
	return out
}

// anyKey returns the smallest key of a non-empty set, so a warning names the
// same IP for the same sets.
func anyKey(set map[string]struct{}) string {
	first := ""
	for k := range set {
		if first == "" || k < first {
			first = k
		}
	}
	return first
}

// checkNATAddr records one handshake's comparison of observed and advertised
// IPs. After advertisedUnderlayRepinThreshold consecutive disagreements it
// logs one warning and counts one mismatch; an agreeing handshake resets the
// run, so a stale nat-addr is reported once per run rather than once per
// handshake.
func (s *Service) checkNATAddr(observed, advertised []ma.Multiaddr) {
	disagrees, observedIP, advertisedIP := natDisagreement(observed, advertised)

	s.advMu.Lock()
	defer s.advMu.Unlock()

	if !disagrees {
		s.natMismatch = 0
		return
	}
	s.natMismatch++
	if s.natMismatch != advertisedUnderlayRepinThreshold {
		return
	}
	s.metrics.NATAddrMismatch.Inc()
	s.logger.Warning("configured nat-addr IP is not the IP peers observe; update nat-addr, or, if this node sends and receives through the same public IP, set it to \":<port>\" to follow the observed IP",
		"advertised_ip", advertisedIP, "observed_ip", observedIP)
}
