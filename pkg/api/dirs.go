// Copyright 2020 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/ethersphere/bee/v2/pkg/accesscontrol"
	"github.com/ethersphere/bee/v2/pkg/file/loadsave"
	"github.com/ethersphere/bee/v2/pkg/file/redundancy"
	"github.com/ethersphere/bee/v2/pkg/jsonhttp"
	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/manifest"
	"github.com/ethersphere/bee/v2/pkg/postage"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/storer"
	"github.com/ethersphere/bee/v2/pkg/swarm"
	"github.com/ethersphere/bee/v2/pkg/tracing"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var (
	errEmptyDir = errors.New("no files in root directory")
	// errInvalidIndexDocument is returned when the Swarm-Index-Document header
	// names a path rather than a file. The header is a suffix appended to a
	// directory path, so a slash in it is rejected rather than used. The request is
	// malformed, which is why this is a sentinel: the handler answers 400 for
	// it rather than reporting a node failure (#366).
	errInvalidIndexDocument = errors.New("index document suffix must not include slash character")
	// wasp #424: mime/multipart reports a missing boundary with an unexported
	// error, so it is caught before the reader is built rather than matched
	// afterwards.
	errNoBoundary = errors.New("content type declares no multipart boundary")
	// wasp #455: mime/multipart builds "expecting a new Part" and its sibling
	// with fmt.Errorf and the caller's own bytes, so neither errors.Is nor
	// errors.As can reach them. multipartReader.Next attaches this instead,
	// which is sound because NextPart is the only call in that function
	// whose error is returned. See the comment there.
	errMalformedMultipart = errors.New("malformed multipart body")
)

// dirUploadHandler uploads a directory supplied as a tar in an HTTP request
func (s *Service) dirUploadHandler(
	ctx context.Context,
	logger log.Logger,
	span trace.Span,
	w http.ResponseWriter,
	r *http.Request,
	putter storer.PutterSession,
	encrypt bool,
	tag uint64,
	rLevel redundancy.Level,
	act bool,
	historyAddress swarm.Address,
) {
	if r.Body == http.NoBody {
		logger.Error(nil, "request has no body")
		jsonhttp.BadRequest(w, errInvalidRequest)
		return
	}

	// Parse error is ignored; unsupported media types are caught by the default case below.
	mediaType, params, _ := mime.ParseMediaType(r.Header.Get(ContentTypeHeader))

	var dReader dirReader
	switch mediaType {
	case contentTypeTar:
		dReader = &tarReader{r: tar.NewReader(r.Body), logger: s.logger}
	case multiPartFormData:
		if params["boundary"] == "" {
			logger.Debug("multipart upload without a boundary", "error", errNoBoundary)
			jsonhttp.BadRequest(w, errNoBoundary)
			return
		}
		dReader = &multipartReader{r: multipart.NewReader(r.Body, params["boundary"])}
	default:
		logger.Error(nil, "invalid content-type for directory upload")
		jsonhttp.BadRequest(w, errInvalidContentType)
		return
	}
	defer r.Body.Close()

	reference, err := storeDir(
		ctx,
		encrypt,
		dReader,
		logger,
		putter,
		s.storer.ChunkStore(),
		r.Header.Get(SwarmIndexDocumentHeader),
		r.Header.Get(SwarmErrorDocumentHeader),
		rLevel,
	)
	if err != nil {
		logger.Debug("store dir failed", "error", err)
		logger.Error(nil, "store dir failed")
		var protoErr textproto.ProtocolError
		switch {
		case errors.Is(err, postage.ErrBucketFull):
			jsonhttp.PaymentRequired(w, "batch is overissued")
		case errors.Is(err, errEmptyDir):
			jsonhttp.BadRequest(w, errEmptyDir)
		case errors.Is(err, errInvalidIndexDocument):
			jsonhttp.BadRequest(w, errInvalidIndexDocument)
		case errors.Is(err, tar.ErrHeader):
			jsonhttp.BadRequest(w, "invalid filename in tar archive")
		case errors.As(err, &protoErr):
			// wasp #424: a malformed part header, such as a line with no
			// colon. textproto.ProtocolError is a string type whose text
			// carries the offending line, so it is matched by type rather
			// than by identity or message.
			jsonhttp.BadRequest(w, "malformed multipart header")
		case errors.Is(err, io.ErrUnexpectedEOF):
			// A body that stops part way through, which includes anything
			// shorter than one 512-byte tar header block. archive/tar reports
			// this differently from a bad header, and both are the caller's
			// fault, so both answer 400. POST /wasp/ingest already answered
			// this way through the same storeDir (#409).
			//
			// This matches wider than the reader. storeDir also wraps the
			// pipeline as "store dir file", so a node-side failure whose
			// chain carried io.ErrUnexpectedEOF would be reported here as a
			// malformed archive. No such source exists in the write path
			// today, checked across pkg/file, pkg/storer, pkg/sharky and
			// pkg/storage, and a body that stops inside an entry arrives
			// through exactly that wrap, which is why the match is not
			// narrowed to the reader. If one ever appears, narrow it.
			jsonhttp.BadRequest(w, "archive ends before it is complete")
		case errors.Is(err, multipart.ErrMessageTooLarge):
			// wasp #455: one part carrying more than 10000 header lines, or
			// more than 10 MB of them. An exported sentinel, so this is
			// reachable by identity, unlike the case below. It is a limit on
			// one part's headers and not on the upload, so a well-formed
			// upload of any size never reaches it.
			jsonhttp.BadRequest(w, "multipart part headers are too large")
		case errors.Is(err, errMalformedMultipart):
			// wasp #455: anything else mime/multipart refused in the caller's
			// body. Last among the multipart cases, so the more precise
			// messages above keep winning: a malformed part header carries
			// both this and textproto.ProtocolError, and the order is what
			// decides which of the two the caller is told.
			jsonhttp.BadRequest(w, "malformed multipart body")
		default:
			jsonhttp.InternalServerError(w, errDirectoryStore)
		}
		tracing.RecordError(span, err, attribute.String("swarm.operation.action", "dir.store"))
		return
	}

	encryptedReference := reference
	historyReference := swarm.ZeroAddress
	if act {
		encryptedReference, historyReference, err = s.actEncryptionHandler(r.Context(), putter, reference, historyAddress, rLevel)
		if err != nil {
			logger.Debug("access control upload failed", "error", err)
			logger.Error(nil, "access control upload failed")
			switch {
			case errors.Is(err, accesscontrol.ErrNotFound):
				jsonhttp.NotFound(w, "act or history entry not found")
			case errors.Is(err, accesscontrol.ErrInvalidPublicKey) || errors.Is(err, accesscontrol.ErrSecretKeyInfinity):
				jsonhttp.BadRequest(w, "invalid public key")
			case errors.Is(err, accesscontrol.ErrUnexpectedType):
				jsonhttp.BadRequest(w, "failed to create history")
			default:
				jsonhttp.InternalServerError(w, errActUpload)
			}
			return
		}
	}

	err = putter.Done(reference)
	if err != nil {
		logger.Debug("store dir failed", "error", err)
		logger.Error(nil, "store dir failed")
		jsonhttp.InternalServerError(w, errDirectoryStore)
		tracing.RecordError(span, err, attribute.String("swarm.operation.action", "putter.Done"))
		return
	}

	if tag != 0 {
		w.Header().Set(SwarmTagHeader, fmt.Sprint(tag))
		span.SetAttributes(attribute.Bool("swarm.operation.success", true))
	}
	w.Header().Set(AccessControlExposeHeaders, SwarmTagHeader)
	if act {
		w.Header().Set(SwarmActHistoryAddressHeader, historyReference.String())
		w.Header().Add(AccessControlExposeHeaders, SwarmActHistoryAddressHeader)
	}
	jsonhttp.Created(w, bzzUploadResponse{
		Reference: encryptedReference,
	})
}

// storeDir stores all files recursively contained in the directory given as a tar/multipart
// it returns the hash for the uploaded manifest corresponding to the uploaded dir
func storeDir(
	ctx context.Context,
	encrypt bool,
	reader dirReader,
	log log.Logger,
	putter storage.Putter,
	getter storage.Getter,
	indexFilename,
	errorFilename string,
	rLevel redundancy.Level,
) (swarm.Address, error) {
	logger := tracing.NewLoggerWithTraceID(ctx, log)
	loggerV1 := logger.V(1).Build()

	p := requestPipelineFn(putter, encrypt, rLevel)
	ls := loadsave.New(getter, putter, requestPipelineFactory(ctx, putter, encrypt, rLevel), rLevel)

	dirManifest, err := manifest.NewDefaultManifest(ls, encrypt)
	if err != nil {
		return swarm.ZeroAddress, err
	}

	if indexFilename != "" && strings.ContainsRune(indexFilename, '/') {
		return swarm.ZeroAddress, errInvalidIndexDocument
	}

	filesAdded := 0

	// iterate through the files in the supplied tar
	for {
		fileInfo, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return swarm.ZeroAddress, fmt.Errorf("read dir stream: %w", err)
		}

		fileReference, err := p(ctx, fileInfo.Reader)
		if err != nil {
			return swarm.ZeroAddress, fmt.Errorf("store dir file: %w", err)
		}
		loggerV1.Debug("bzz upload dir: file dir uploaded", "file_path", fileInfo.Path, "address", fileReference)

		fileMtdt := map[string]string{
			manifest.EntryMetadataContentTypeKey: fileInfo.ContentType,
			manifest.EntryMetadataFilenameKey:    fileInfo.Name,
		}
		// add file entry to dir manifest
		err = dirManifest.Add(ctx, fileInfo.Path, manifest.NewEntry(fileReference, fileMtdt))
		if err != nil {
			return swarm.ZeroAddress, fmt.Errorf("add to manifest: %w", err)
		}

		filesAdded++
	}

	// check if files were uploaded through the manifest
	if filesAdded == 0 {
		return swarm.ZeroAddress, errEmptyDir
	}

	// store website information
	if indexFilename != "" || errorFilename != "" {
		metadata := map[string]string{}
		if indexFilename != "" {
			metadata[manifest.WebsiteIndexDocumentSuffixKey] = indexFilename
		}
		if errorFilename != "" {
			metadata[manifest.WebsiteErrorDocumentPathKey] = errorFilename
		}
		rootManifestEntry := manifest.NewEntry(swarm.ZeroAddress, metadata)
		err = dirManifest.Add(ctx, manifest.RootPath, rootManifestEntry)
		if err != nil {
			return swarm.ZeroAddress, fmt.Errorf("add to manifest: %w", err)
		}
	}

	// save manifest
	manifestReference, err := dirManifest.Store(ctx)
	if err != nil {
		return swarm.ZeroAddress, fmt.Errorf("store manifest: %w", err)
	}
	loggerV1.Debug("bzz upload dir: uploaded dir finished", "address", manifestReference)

	return manifestReference, nil
}

type FileInfo struct {
	Path        string
	Name        string
	ContentType string
	Size        int64
	Reader      io.Reader
}

type dirReader interface {
	Next() (*FileInfo, error)
}

type tarReader struct {
	r      *tar.Reader
	logger log.Logger
}

func (t *tarReader) Next() (*FileInfo, error) {
	for {
		fileHeader, err := t.r.Next()
		if err != nil {
			return nil, err
		}

		fileName := fileHeader.FileInfo().Name()
		contentType := mime.TypeByExtension(filepath.Ext(fileHeader.Name))
		fileSize := fileHeader.FileInfo().Size()
		filePath := filepath.Clean(fileHeader.Name)

		if filePath == "." {
			t.logger.Warning("skipping file upload empty path")
			continue
		}
		if runtime.GOOS == "windows" {
			// always use Unix path separator
			filePath = filepath.ToSlash(filePath)
		}
		// only store regular files
		if !fileHeader.FileInfo().Mode().IsRegular() {
			t.logger.Warning("bzz upload dir: skipping file upload as it is not a regular file", "file_path", filePath)
			continue
		}

		return &FileInfo{
			Path:        filePath,
			Name:        fileName,
			ContentType: contentType,
			Size:        fileSize,
			Reader:      t.r,
		}, nil
	}
}

// multipart reader returns files added as a multipart form. We will ensure all the
// part headers are passed correctly
type multipartReader struct {
	r *multipart.Reader
}

func (m *multipartReader) Next() (*FileInfo, error) {
	part, err := m.r.NextPart()
	if err != nil {
		if errors.Is(err, io.EOF) {
			// The normal end of the parts, which storeDir's loop tests for,
			// so it is passed through unwrapped: a sentinel has no business
			// in the chain of something that is not an error. Note that
			// mime/multipart also reports several malformed bodies this way,
			// wrapping a read error as "multipart: NextPart: %w"; those
			// already answer 400 through errEmptyDir and are unaffected.
			return nil, err
		}
		// wasp #455: every other error from NextPart comes from parsing the
		// bytes the caller sent or from reading the caller's body. That is a
		// property of this call site rather than of any individual error,
		// which is why the sentinel can be attached here and could not be
		// inferred from the errors themselves. One of them, "expecting a new
		// Part", is a bare fmt.Errorf with the caller's own bytes quoted into
		// it and is reachable no other way.
		//
		// Precisely: NextPart is the only call in this function whose error
		// is returned. The others either cannot fail or, in ParseInt's case,
		// have their error discarded. It is that, and not a claim that this
		// function calls nothing else, which makes the attribution sound.
		//
		// Two further things would break it. If this function ever returns an
		// error from a second call, to this node's own storage for example,
		// the sentinel stops being a statement about the caller. And if the
		// server ever gains a read deadline on the body, node.go sets only
		// ReadHeaderTimeout today and no MaxBytesReader is used here, a
		// timeout would arrive through NextPart looking like the caller's
		// fault.
		//
		// Two %w verbs, so the chains the handler already matches stay
		// reachable through the wrap and keep their more precise messages.
		return nil, fmt.Errorf("%w: %w", errMalformedMultipart, err)
	}

	filePath := part.FileName()
	if filePath == "" {
		filePath = part.FormName()
	}

	fileName := filepath.Base(filePath)

	contentType := part.Header.Get(ContentTypeHeader)

	contentLength := part.Header.Get(ContentLengthHeader)

	fileSize, _ := strconv.ParseInt(contentLength, 10, 64)

	return &FileInfo{
		Path:        filePath,
		Name:        fileName,
		ContentType: contentType,
		Size:        fileSize,
		Reader:      part,
	}, nil
}
