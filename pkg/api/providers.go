// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ethersphere/bee/v2/pkg/jsonhttp"
	"github.com/ethersphere/bee/v2/pkg/postage"
	"github.com/ethersphere/bee/v2/pkg/providers"
	"github.com/ethersphere/bee/v2/pkg/retrieval"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	"github.com/gorilla/mux"
)

// WaspProvidersHeader names providers, by overlay, that a download tries
// first. It is honored only when content providers are on.
const WaspProvidersHeader = "Wasp-Providers"

const (
	// maxProviderHints is the most entries of WaspProvidersHeader used.
	maxProviderHints = 8
	// discoverAfterChunks is how many chunks a download fetches before it
	// looks up providers of its content. Smaller downloads cannot gain
	// enough to pay for a lookup.
	discoverAfterChunks = 64
	// providerSetTTL is how long the preferred set of a content key is shared
	// by its downloads, the same as the lookup cache.
	providerSetTTL = 10 * time.Minute
	// maxProviderSets bounds the number of shared preferred sets.
	maxProviderSets = 1024
)

var errProvidersHeader = errors.New("invalid Wasp-Providers header: want comma-separated hex overlays")

// Providers is the content-providers service.
type Providers interface {
	Announce(ctx context.Context, k, batchID []byte) error
	Withdraw(k []byte) error
	Announced() ([]providers.Announcement, error)
	Lookup(ctx context.Context, k []byte) ([]*providers.Record, error)
	Discover(ctx context.Context, k []byte, set providers.Adder)
	ConnectHints(ctx context.Context, overlays []swarm.Address)
}

type providerHintKey struct{}

// providerHint is what a download carries for content providers: its
// preferred set, its content key, and how many chunks it has fetched.
type providerHint struct {
	set     *retrieval.PreferredSet
	key     []byte
	fetched atomic.Int64
}

// withProviders prepares a download for content providers when they are on.
// It puts a preferred set into the request context, filled from
// WaspProvidersHeader and, for a download of content key k, by a lookup once
// the download is large enough. A request that already carries a set, such as
// the manifest entry of a /bzz download, keeps it, so the lookup uses the
// manifest root as the content key.
func (s *Service) withProviders(r *http.Request, k []byte) (*http.Request, error) {
	if s.providers == nil {
		return r, nil
	}
	if _, ok := r.Context().Value(providerHintKey{}).(*providerHint); ok {
		return r, nil
	}

	overlays, err := parseProviderHints(r.Header.Get(WaspProvidersHeader))
	if err != nil {
		return nil, err
	}

	// an encrypted reference carries its decryption key; only plain
	// references are looked up
	if len(k) != swarm.HashSize {
		k = nil
	}

	// an explicit hint applies to this request only; otherwise the discovered
	// providers, and which of them were dropped, are shared by all downloads
	// of the same content key
	set := retrieval.NewPreferredSet(overlays...)
	if len(overlays) == 0 && k != nil {
		set = s.providerSet(k)
	}
	hint := &providerHint{set: set, key: k}
	ctx := retrieval.WithPreferredPeers(r.Context(), hint.set)
	ctx = context.WithValue(ctx, providerHintKey{}, hint)
	if len(overlays) > 0 {
		s.providers.ConnectHints(ctx, overlays)
	}
	return r.WithContext(ctx), nil
}

// providerGetter wraps the getter of a download so that, once the download
// has fetched discoverAfterChunks chunks, providers of its content key are
// looked up and added to its preferred set.
func (s *Service) providerGetter(ctx context.Context, g storage.Getter) storage.Getter {
	hint, ok := ctx.Value(providerHintKey{}).(*providerHint)
	if !ok || s.providers == nil || hint.key == nil {
		return g
	}
	return storage.GetterFunc(func(ctx context.Context, addr swarm.Address) (swarm.Chunk, error) {
		if hint.fetched.Add(1) == discoverAfterChunks {
			// the lookup's own reads must not go to this download's
			// preferred peers
			s.providers.Discover(retrieval.WithPreferredPeers(ctx, nil), hint.key, hint.set)
		}
		return g.Get(ctx, addr)
	})
}

type providerSetEntry struct {
	set     *retrieval.PreferredSet
	expires time.Time
}

// providerSet returns the preferred set shared by all downloads of content
// key k for providerSetTTL, so that discovered providers, and the providers
// dropped for missing chunks, carry over from one request to the next.
func (s *Service) providerSet(k []byte) *retrieval.PreferredSet {
	s.providerSetsMu.Lock()
	defer s.providerSetsMu.Unlock()

	now := time.Now()
	if e, ok := s.providerSets[string(k)]; ok && now.Before(e.expires) {
		return e.set
	}
	if s.providerSets == nil {
		s.providerSets = make(map[string]providerSetEntry)
	}
	if len(s.providerSets) >= maxProviderSets {
		for key, e := range s.providerSets {
			if !now.Before(e.expires) {
				delete(s.providerSets, key)
			}
		}
		for key := range s.providerSets {
			if len(s.providerSets) < maxProviderSets {
				break
			}
			delete(s.providerSets, key)
		}
	}

	set := retrieval.NewPreferredSet()
	s.providerSets[string(k)] = providerSetEntry{set: set, expires: now.Add(providerSetTTL)}
	return set
}

// parseProviderHints parses WaspProvidersHeader. Only overlays are accepted,
// so that a download request cannot make the node dial an arbitrary network
// address. At most maxProviderHints distinct entries are used.
func parseProviderHints(v string) ([]swarm.Address, error) {
	var overlays []swarm.Address
	for _, e := range strings.Split(v, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		a, err := swarm.ParseHexAddress(e)
		if err != nil || len(a.Bytes()) != swarm.HashSize {
			return nil, errProvidersHeader
		}
		if len(overlays) < maxProviderHints && !swarm.ContainsAddress(overlays, a) {
			overlays = append(overlays, a)
		}
	}
	return overlays, nil
}

type providerAnnouncementResponse struct {
	Reference string   `json:"reference"`
	BatchID   string   `json:"batchID"`
	Windows   []uint64 `json:"windows"`
}

type providerAnnouncementsResponse struct {
	Announcements []providerAnnouncementResponse `json:"announcements"`
}

type providerResponse struct {
	Overlay    swarm.Address `json:"overlay"`
	Underlays  []string      `json:"underlays"`
	Owner      string        `json:"owner"`
	Capability string        `json:"capability"`
}

type providerLookupResponse struct {
	Providers []providerResponse `json:"providers"`
}

// providersEnabled writes 403 and returns false when content providers are
// off.
func (s *Service) providersEnabled(w http.ResponseWriter) bool {
	if s.providers == nil {
		jsonhttp.Forbidden(w, "content providers are off; start the node with providers-enable")
		return false
	}
	return true
}

func (s *Service) providersAnnounceHandler(w http.ResponseWriter, r *http.Request) {
	logger := s.logger.WithName("post_providers").Build()

	if !s.providersEnabled(w) {
		return
	}
	if s.beeMode != FullMode {
		jsonhttp.BadRequest(w, "only a full node can announce content")
		return
	}

	paths := struct {
		Reference swarm.Address `map:"reference" validate:"required"`
	}{}
	if response := s.mapStructure(mux.Vars(r), &paths); response != nil {
		response("invalid path params", logger, w)
		return
	}
	if len(paths.Reference.Bytes()) != swarm.HashSize {
		jsonhttp.BadRequest(w, "encrypted references cannot be announced: a record would publish their key")
		return
	}

	headers := struct {
		BatchID []byte `map:"Swarm-Postage-Batch-Id" validate:"required"`
	}{}
	if response := s.mapStructure(r.Header, &headers); response != nil {
		response("invalid header params", logger, w)
		return
	}

	pinned, err := s.storer.HasPin(paths.Reference)
	if err != nil {
		logger.Debug("has pin failed", "reference", paths.Reference, "error", err)
		logger.Error(nil, "has pin failed")
		jsonhttp.InternalServerError(w, "checking the pin failed")
		return
	}
	if !pinned {
		jsonhttp.BadRequest(w, "reference is not pinned; pin it first with POST /pins/{reference}")
		return
	}

	if err := s.providers.Announce(r.Context(), paths.Reference.Bytes(), headers.BatchID); err != nil {
		logger.Debug("announce failed", "reference", paths.Reference, "error", err)
		logger.Error(nil, "announce failed")
		switch {
		case errors.Is(err, postage.ErrNotUsable), errors.Is(err, errBatchUnusable):
			jsonhttp.UnprocessableEntity(w, "batch not usable yet or does not exist")
		case errors.Is(err, postage.ErrNotFound):
			jsonhttp.NotFound(w, "batch with id not found")
		default:
			jsonhttp.InternalServerError(w, "announce failed")
		}
		return
	}

	jsonhttp.Created(w, nil)
}

func (s *Service) providersWithdrawHandler(w http.ResponseWriter, r *http.Request) {
	logger := s.logger.WithName("delete_providers").Build()

	if !s.providersEnabled(w) {
		return
	}

	paths := struct {
		Reference swarm.Address `map:"reference" validate:"required"`
	}{}
	if response := s.mapStructure(mux.Vars(r), &paths); response != nil {
		response("invalid path params", logger, w)
		return
	}

	if err := s.providers.Withdraw(paths.Reference.Bytes()); err != nil {
		logger.Debug("withdraw failed", "reference", paths.Reference, "error", err)
		logger.Error(nil, "withdraw failed")
		jsonhttp.InternalServerError(w, "withdraw failed")
		return
	}

	jsonhttp.OK(w, nil)
}

func (s *Service) providersListHandler(w http.ResponseWriter, _ *http.Request) {
	logger := s.logger.WithName("get_providers").Build()

	if !s.providersEnabled(w) {
		return
	}

	announced, err := s.providers.Announced()
	if err != nil {
		logger.Debug("list announcements failed", "error", err)
		logger.Error(nil, "list announcements failed")
		jsonhttp.InternalServerError(w, "list announcements failed")
		return
	}

	resp := providerAnnouncementsResponse{Announcements: make([]providerAnnouncementResponse, 0, len(announced))}
	for _, a := range announced {
		windows := a.Written
		if windows == nil {
			windows = []uint64{}
		}
		resp.Announcements = append(resp.Announcements, providerAnnouncementResponse{
			Reference: hex.EncodeToString(a.Key),
			BatchID:   hex.EncodeToString(a.BatchID),
			Windows:   windows,
		})
	}
	jsonhttp.OK(w, resp)
}

func (s *Service) providersLookupHandler(w http.ResponseWriter, r *http.Request) {
	logger := s.logger.WithName("get_providers_lookup").Build()

	if !s.providersEnabled(w) {
		return
	}

	paths := struct {
		Reference swarm.Address `map:"reference" validate:"required"`
	}{}
	if response := s.mapStructure(mux.Vars(r), &paths); response != nil {
		response("invalid path params", logger, w)
		return
	}

	if len(paths.Reference.Bytes()) != swarm.HashSize {
		jsonhttp.BadRequest(w, "encrypted references are not looked up")
		return
	}

	records, err := s.providers.Lookup(r.Context(), paths.Reference.Bytes())
	if err != nil {
		logger.Debug("provider lookup failed", "reference", paths.Reference, "error", err)
		logger.Error(nil, "provider lookup failed")
		jsonhttp.InternalServerError(w, "provider lookup failed")
		return
	}

	resp := providerLookupResponse{Providers: make([]providerResponse, 0, len(records))}
	for _, rec := range records {
		underlays := make([]string, 0, len(rec.Address.Underlays))
		for _, u := range rec.Address.Underlays {
			underlays = append(underlays, u.String())
		}
		resp.Providers = append(resp.Providers, providerResponse{
			Overlay:    rec.Address.Overlay,
			Underlays:  underlays,
			Owner:      hex.EncodeToString(rec.Owner),
			Capability: string(rec.Capability),
		})
	}
	jsonhttp.OK(w, resp)
}
