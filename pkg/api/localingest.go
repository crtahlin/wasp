// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"sync"

	"github.com/ethersphere/bee/v2/pkg/file/redundancy"
	"github.com/ethersphere/bee/v2/pkg/jsonhttp"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/storer"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// localIngestWarnAt is the fraction of the limit at which an ingest logs a
// warning the operator will see. Compiled in under rule 8: it becomes a
// setting only if a measurement shows the value matters.
const localIngestWarnAt = 0.9

type localIngestResponse struct {
	Reference swarm.Address `json:"reference"`
	// Chunks is the number of distinct chunks stored, which is what counts
	// against the limit.
	Chunks uint64 `json:"chunks"`
	// SoleSource records that at the moment of ingest no other node held
	// this content. It stops being true once anyone retrieves it through a
	// forwarding peer, which caches what it relays.
	SoleSource bool `json:"soleSource"`
}

type localIngestFullResponse struct {
	Message string `json:"message"`
	// Held is the node-wide count of chunks from local ingests. It is
	// deliberately not called "chunks": that name means this ingest's own
	// count in the 201 body, and one name for two quantities is how a
	// reader ends up acting on the wrong one.
	Held  uint64 `json:"held"`
	Limit uint64 `json:"limit"`
}

// localIngestHandler stores content in this node's own store without postage
// and without pushing it to the network. See issue #326.
//
// It is the raw-bytes upload path with the postage machinery removed. The
// splitter takes a bare putter and stamping is a property of the putter, so
// the pinning collection an ordinary pin already uses serves unchanged.
func (s *Service) localIngestHandler(w http.ResponseWriter, r *http.Request) {
	logger := s.logger.WithName("post_wasp_ingest").Build()

	if !s.localIngestEnabled {
		jsonhttp.Forbidden(w, "local ingest is off; start the node with local-ingest-enable")
		return
	}

	headers := struct {
		Encrypt bool              `map:"Swarm-Encrypt"`
		RLevel  *redundancy.Level `map:"Swarm-Redundancy-Level" validate:"omitempty,rLevel"`
		IsDir   bool              `map:"Swarm-Collection"`
	}{}
	if response := s.mapStructure(r.Header, &headers); response != nil {
		response("invalid header params", logger, w)
		return
	}

	// A directory is selected by the header alone, deliberately not by the
	// content type the way /bzz also does it. Dispatching on a multipart or tar
	// content type would silently change what an existing request means: a tar
	// posted here today is stored as a blob, and would become its contents.
	// See docs/experiments/content-providers/directory-ingest.md.
	var dReader dirReader
	if headers.IsDir {
		if r.Body == http.NoBody {
			logger.Error(nil, "local ingest: request has no body")
			jsonhttp.BadRequest(w, errInvalidRequest)
			return
		}
		// The parse error is ignored; an unsupported type falls to the default.
		mediaType, params, _ := mime.ParseMediaType(r.Header.Get(ContentTypeHeader))
		switch mediaType {
		case contentTypeTar:
			dReader = &tarReader{r: tar.NewReader(r.Body), logger: logger}
		case multiPartFormData:
			dReader = &multipartReader{r: multipart.NewReader(r.Body, params["boundary"])}
		default:
			logger.Error(nil, "local ingest: invalid content-type for a collection")
			jsonhttp.BadRequest(w, errInvalidContentType)
			return
		}
		defer r.Body.Close()

		// Without a root index document GET /bzz/{ref}/ answers 404 even with
		// every chunk held locally, so a site ingested without one is reachable
		// only by full path. Worth a warning rather than a refusal: serving by
		// full path is a legitimate use.
		if r.Header.Get(SwarmIndexDocumentHeader) == "" {
			logger.Warning("local ingest: collection has no index document, its bare root will not be servable")
		}
	}

	// DefaultUploadLevel, not DefaultDownloadLevel. Pinning uses the download
	// one, and copying pinning here would break address equivalence with
	// POST /bytes for a reason unrelated to this feature.
	rLevel := redundancy.DefaultUploadLevel
	if headers.RLevel != nil {
		rLevel = *headers.RLevel
	}

	committed, reserved, limit := s.storer.LocalIngestUsage()

	// A refusal on a declared length saves reading a body that cannot fit.
	// It is a convenience and not the enforcement: Content-Length is absent
	// under chunked transfer encoding and is client-supplied in any case.
	//
	// Skipped entirely for a collection. CalculateNumberOfChunks models a flat
	// blob, so for an archive it knows nothing about tar or multipart framing
	// and nothing about the manifest node chunks, which for many small files
	// outweigh the file chunks themselves. A check that returns a confident
	// wrong answer is worse than no check; the mid-stream claim is the real
	// enforcement either way.
	if !headers.IsDir && limit > 0 && r.ContentLength > 0 {
		want := uint64(CalculateNumberOfChunks(r.ContentLength, headers.Encrypt))
		if committed+reserved+want > limit {
			logger.Warning("local ingest refused, declared length does not fit under the limit", "limit", limit, "held_chunks", committed, "wanted", want)
			respondLocalIngestFull(w, committed, limit)
			return
		}
	}

	session, err := s.storer.NewLocalIngestCollection(r.Context())
	if err != nil {
		logger.Debug("local ingest: new collection failed", "error", err)
		logger.Error(nil, "local ingest: new collection failed")
		jsonhttp.InternalServerError(w, "local ingest: create collection failed")
		return
	}

	// cleanupOnErrWriter wraps the RESPONSE WRITER and fires onErr from
	// WriteHeader on any status of 400 or above. So every error path below
	// must answer through ow: a path that answers through w leaves a dirty
	// collection whose chunks nothing removes until the next restart.
	ow := &cleanupOnErrWriter{
		ResponseWriter: w,
		onErr:          session.Cleanup,
		logger:         logger,
	}

	counted := newCountingPutter(session)

	// One session for everything. The file chunks, every manifest node chunk
	// and the root all go through the same counting putter, so manifest chunks
	// count against the limit as they must: they are chunks on the same disk.
	// storeDir takes a bare Putter and a bare Getter and no stamper, which is
	// what makes the directory path work here unchanged.
	var reference swarm.Address
	if headers.IsDir {
		reference, err = storeDir(
			r.Context(),
			headers.Encrypt,
			dReader,
			logger,
			counted,
			s.storer.ChunkStore(),
			r.Header.Get(SwarmIndexDocumentHeader),
			r.Header.Get(SwarmErrorDocumentHeader),
			rLevel,
		)
	} else {
		p := requestPipelineFn(counted, headers.Encrypt, rLevel)
		reference, err = p(r.Context(), r.Body)
	}
	if err != nil {
		// The limit is reported out of band rather than by errors.Is,
		// because hashtrie.Sum formats the dispersed-replica failure with
		// %s against err.Error() and so discards the chain. That put
		// happens after the whole body has been read, which is exactly
		// where a large ingest crosses its limit. See issue #337.
		if counted.exceededLimit() {
			// Read the usage again rather than reusing the figure from
			// before the body: another ingest may have committed since.
			held, _, limit := s.storer.LocalIngestUsage()
			logger.Warning("local ingest refused, limit reached", "limit", limit, "held_chunks", held)
			respondLocalIngestFull(ow, held, limit)
			return
		}
		// A malformed archive is the caller's fault, not this node's. Both
		// answer through ow, like every other failure here, so the collection
		// is released rather than left on disk until the next restart.
		switch {
		case errors.Is(err, errEmptyDir):
			logger.Debug("local ingest: collection has no files", "error", err)
			jsonhttp.BadRequest(ow, errEmptyDir)
			return
		case errors.Is(err, tar.ErrHeader):
			logger.Debug("local ingest: invalid tar header", "error", err)
			jsonhttp.BadRequest(ow, "invalid tar archive")
			return
		case errors.Is(err, io.ErrUnexpectedEOF):
			// A body that stops mid-archive, which includes anything shorter
			// than one 512-byte tar header block. archive/tar reports the two
			// cases differently and both are the caller's fault, so both are
			// 400. The stamped route answers 500 for this one; it is unmodified
			// upstream code and out of scope here.
			logger.Debug("local ingest: archive ends early", "error", err)
			jsonhttp.BadRequest(ow, "archive ends before it is complete")
			return
		}
		logger.Debug("local ingest: split write all failed", "error", err)
		logger.Error(nil, "local ingest: split write all failed")
		jsonhttp.InternalServerError(ow, "local ingest: split write all failed")
		return
	}

	chunks := counted.distinct()

	if err := session.Done(reference, chunks); err != nil {
		// Already held. The reference could not be checked up front the way
		// pinning does, because it is not known until the body is split.
		if errors.Is(err, storer.ErrLocalIngestDuplicate) {
			if err := session.Cleanup(); err != nil {
				logger.Debug("local ingest: cleanup after duplicate failed", "error", err)
				logger.Error(nil, "local ingest: cleanup after a duplicate failed, its chunks stay on disk counted by nothing until the next restart")
			}
			jsonhttp.OK(w, localIngestResponse{
				Reference:  reference,
				Chunks:     chunks,
				SoleSource: false,
			})
			return
		}
		logger.Debug("local ingest: done failed", "error", err)
		logger.Error(nil, "local ingest: done failed")
		jsonhttp.InternalServerError(ow, "local ingest: done failed")
		return
	}

	now, _, limit := s.storer.LocalIngestUsage()

	// A warning below the limit, not only at it, so an operator has notice
	// before an ingest is refused.
	if limit > 0 && float64(now) >= float64(limit)*localIngestWarnAt {
		logger.Warning("local ingest is close to its limit", "chunks", now, "limit", limit)
	}

	logger.Debug("local ingest stored", "reference", reference, "chunks", chunks)

	jsonhttp.Created(w, localIngestResponse{
		Reference:  reference,
		Chunks:     chunks,
		SoleSource: true,
	})
}

func respondLocalIngestFull(w http.ResponseWriter, held, limit uint64) {
	jsonhttp.Respond(w, http.StatusInsufficientStorage, localIngestFullResponse{
		Message: "local ingest limit reached",
		Held:    held,
		Limit:   limit,
	})
}

// countingPutter counts the distinct chunk addresses a session stores and
// claims room for each against the node-wide limit.
//
// The count cannot come from the pipeline, which feeds to end of input before
// returning anything, nor be read back from the collection, whose stat cannot
// be matched to a root through any exported reader and would not be visible
// inside the committing transaction anyway. Counting distinct addresses here
// gives exactly the collection's own Total minus DupInCollection, so the
// mid-stream limit and the recorded usage are one quantity rather than two
// that can disagree.
type countingPutter struct {
	session storer.LocalIngestSession

	mu       sync.Mutex
	seen     map[[swarm.HashSize]byte]struct{}
	count    uint64
	exceeded bool
}

var _ storage.Putter = (*countingPutter)(nil)

func newCountingPutter(session storer.LocalIngestSession) *countingPutter {
	return &countingPutter{
		session: session,
		seen:    make(map[[swarm.HashSize]byte]struct{}),
	}
}

// Put is called from several goroutines when the dispersed root replicas are
// stored, so everything it touches is under the mutex.
func (c *countingPutter) Put(ctx context.Context, chunk swarm.Chunk) error {
	if err := c.admit(chunk.Address()); err != nil {
		return err
	}
	return c.session.Put(ctx, chunk)
}

// admit claims room for an address that has not been seen before. The claim is
// taken under this mutex so two goroutines cannot both find the same address
// absent and both claim for it.
//
// Taking the session's claim while holding this mutex is safe: the claim
// touches only the node-wide counters and never the storer, so it cannot reach
// back for a lock this one is inside.
func (c *countingPutter) admit(addr swarm.Address) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	key, keyed := hashKey(addr)
	if keyed {
		if _, ok := c.seen[key]; ok {
			return nil
		}
	}

	if err := c.session.Reserve(1); err != nil {
		if errors.Is(err, storer.ErrLocalIngestLimit) {
			c.exceeded = true
		}
		return err
	}

	if keyed {
		c.seen[key] = struct{}{}
	}
	c.count++
	return nil
}

func (c *countingPutter) distinct() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.count
}

// exceededLimit reports whether any put was refused for want of room. The
// handler asks directly rather than inspecting the returned error, which the
// pipeline may have flattened.
func (c *countingPutter) exceededLimit() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.exceeded
}

// hashKey turns a chunk address into a map key. A chunk address is always
// swarm.HashSize bytes; anything else is counted as distinct rather than
// rejected, which can only overcount and so cannot let an ingest past its
// limit.
func hashKey(addr swarm.Address) (key [swarm.HashSize]byte, ok bool) {
	b := addr.Bytes()
	if len(b) != swarm.HashSize {
		return key, false
	}
	copy(key[:], b)
	return key, true
}
