// Copyright 2020 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api_test

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path"
	"strconv"
	"strings"
	"testing"

	"github.com/ethersphere/bee/v2/pkg/api"
	"github.com/ethersphere/bee/v2/pkg/file/loadsave"
	"github.com/ethersphere/bee/v2/pkg/file/redundancy"
	"github.com/ethersphere/bee/v2/pkg/jsonhttp"
	"github.com/ethersphere/bee/v2/pkg/jsonhttp/jsonhttptest"
	"github.com/ethersphere/bee/v2/pkg/manifest"
	mockpost "github.com/ethersphere/bee/v2/pkg/postage/mock"
	mockstorer "github.com/ethersphere/bee/v2/pkg/storer/mock"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

// nolint:paralleltest
func TestDirs(t *testing.T) {
	var (
		dirUploadResource   = "/bzz"
		bzzDownloadResource = func(addr, path string) string { return "/bzz/" + addr + "/" + path }
		ctx                 = context.Background()
		storer              = mockstorer.New()
		client, _, _, _     = newTestServer(t, testServerOptions{
			Storer:          storer,
			PreventRedirect: true,
			Post:            mockpost.New(mockpost.WithAcceptAll()),
		})
	)

	t.Run("empty request body", func(t *testing.T) {
		jsonhttptest.Request(t, client, http.MethodPost, dirUploadResource,
			http.StatusBadRequest,
			jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
			jsonhttptest.WithRequestBody(bytes.NewReader(nil)),
			jsonhttptest.WithRequestHeader(api.SwarmCollectionHeader, "True"),
			jsonhttptest.WithExpectedJSONResponse(jsonhttp.StatusResponse{
				Message: api.ErrInvalidRequest.Error(),
				Code:    http.StatusBadRequest,
			}),
			jsonhttptest.WithRequestHeader(api.ContentTypeHeader, api.ContentTypeTar),
		)
	})

	// A body that is not a usable archive is the caller's fault, not this
	// node's, so it is 400. This case asserted 500 until #409; the fork's own
	// POST /wasp/ingest already answered 400 for the same bodies through the
	// same storeDir, and the two routes should not disagree.
	//
	// The four cases are not one case four times, and which message each gets
	// was established by running them rather than assumed. archive/tar reports
	// a body shorter than one 512-byte header block, and a body that stops
	// inside an entry, as an unexpected end of input; a full-size block of
	// nonsense is an invalid header, whether it stands alone or follows a
	// valid entry. So they reach two different branches of the handler's
	// switch, and the entry case reaches its branch through a different
	// wrapping than the other three.
	//
	// A first version of this test carried only the nine byte body, and a
	// second assumed a valid entry followed by garbage would be an unexpected
	// end of input. It is not.
	for _, tc := range []struct {
		name    string
		body    func(t *testing.T) *bytes.Buffer
		message string
	}{
		{"non tar file", tarShorterThanHeader, "archive ends before it is complete"},
		{"stops inside an entry", tarShortEntry, "archive ends before it is complete"},
		{"valid entry then a block of nonsense", tarThenGarbage, "invalid filename in tar archive"},
		{"invalid tar header", tarInvalidHeader, "invalid filename in tar archive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jsonhttptest.Request(t, client, http.MethodPost, dirUploadResource,
				http.StatusBadRequest,
				jsonhttptest.WithRequestHeader(api.SwarmDeferredUploadHeader, "true"),
				jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
				jsonhttptest.WithRequestBody(tc.body(t)),
				jsonhttptest.WithRequestHeader(api.SwarmCollectionHeader, "True"),
				jsonhttptest.WithExpectedJSONResponse(jsonhttp.StatusResponse{
					Message: tc.message,
					Code:    http.StatusBadRequest,
				}),
				jsonhttptest.WithRequestHeader(api.ContentTypeHeader, api.ContentTypeTar),
			)
		})
	}

	t.Run("wrong content type", func(t *testing.T) {
		tarReader := tarFiles(t, []f{{
			data: []byte("some data"),
			name: "binary-file",
		}})

		// submit valid tar, but with wrong content-type
		jsonhttptest.Request(t, client, http.MethodPost, dirUploadResource,
			http.StatusBadRequest,
			jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
			jsonhttptest.WithRequestBody(tarReader),
			jsonhttptest.WithRequestHeader(api.SwarmCollectionHeader, "True"),
			jsonhttptest.WithExpectedJSONResponse(jsonhttp.StatusResponse{
				Message: api.ErrInvalidContentType.Error(),
				Code:    http.StatusBadRequest,
			}),
			jsonhttptest.WithRequestHeader(api.ContentTypeHeader, "other"),
		)
	})

	// A Swarm-Index-Document header naming a path rather than a file is a
	// malformed request, and was answered with 500 until #366. The two cases
	// below are a pair: the first is the defect, the second is the guard that
	// the fix did not turn a working header into a rejected one.
	t.Run("index document with a slash", func(t *testing.T) {
		tarReader := tarFiles(t, []f{{
			data: []byte("some data"),
			name: "index.html",
		}})

		jsonhttptest.Request(t, client, http.MethodPost, dirUploadResource,
			http.StatusBadRequest,
			jsonhttptest.WithRequestHeader(api.SwarmDeferredUploadHeader, "true"),
			jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
			jsonhttptest.WithRequestBody(tarReader),
			jsonhttptest.WithRequestHeader(api.SwarmCollectionHeader, "True"),
			jsonhttptest.WithRequestHeader(api.SwarmIndexDocumentHeader, "dir/index.html"),
			jsonhttptest.WithExpectedJSONResponse(jsonhttp.StatusResponse{
				Message: api.ErrInvalidIndexDocument.Error(),
				Code:    http.StatusBadRequest,
			}),
			jsonhttptest.WithRequestHeader(api.ContentTypeHeader, api.ContentTypeTar),
		)
	})

	t.Run("index document without a slash is accepted", func(t *testing.T) {
		tarReader := tarFiles(t, []f{{
			data: []byte("some data"),
			name: "index.html",
		}})

		jsonhttptest.Request(t, client, http.MethodPost, dirUploadResource,
			http.StatusCreated,
			jsonhttptest.WithRequestHeader(api.SwarmDeferredUploadHeader, "true"),
			jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
			jsonhttptest.WithRequestBody(tarReader),
			jsonhttptest.WithRequestHeader(api.SwarmCollectionHeader, "True"),
			jsonhttptest.WithRequestHeader(api.SwarmIndexDocumentHeader, "index.html"),
			jsonhttptest.WithRequestHeader(api.ContentTypeHeader, api.ContentTypeTar),
		)
	})

	// valid tars
	for _, tc := range []struct {
		name                string
		expectedReference   swarm.Address
		encrypt             bool
		wantIndexFilename   string
		wantErrorFilename   string
		indexFilenameOption jsonhttptest.Option
		errorFilenameOption jsonhttptest.Option
		doMultipart         bool
		files               []f // files in dir for test case
	}{
		{
			name:              "non-nested files without extension",
			expectedReference: swarm.MustParseHexAddress("f3312af64715d26b5e1a3dc90f012d2c9cc74a167899dab1d07cdee8c107f939"),
			files: []f{
				{
					data: []byte("first file data"),
					name: "file1",
					dir:  "",
					header: http.Header{
						api.ContentTypeHeader: {""},
					},
				},
				{
					data: []byte("second file data"),
					name: "file2",
					dir:  "",
					header: http.Header{
						api.ContentTypeHeader: {""},
					},
				},
			},
		},
		{
			name:              "nested files with extension",
			doMultipart:       true,
			expectedReference: swarm.MustParseHexAddress("4c9c76d63856102e54092c38a7cd227d769752d768b7adc8c3542e3dd9fcf295"),
			files: []f{
				{
					data: []byte("robots text"),
					name: "robots.txt",
					dir:  "",
					header: http.Header{
						api.ContentTypeHeader: {"text/plain; charset=utf-8"},
					},
				},
				{
					data: []byte("image 1"),
					name: "1.png",
					dir:  "img",
					header: http.Header{
						api.ContentTypeHeader: {"image/png"},
					},
				},
				{
					data: []byte("image 2"),
					name: "2.png",
					dir:  "img",
					header: http.Header{
						api.ContentTypeHeader: {"image/png"},
					},
				},
			},
		},
		{
			name:              "no index filename",
			expectedReference: swarm.MustParseHexAddress("9e178dbd1ed4b748379e25144e28dfb29c07a4b5114896ef454480115a56b237"),
			doMultipart:       true,
			files: []f{
				{
					data: []byte("<h1>Swarm"),
					name: "index.html",
					dir:  "",
					header: http.Header{
						api.ContentTypeHeader: {"text/html; charset=utf-8"},
					},
				},
			},
		},
		{
			name:                "explicit index filename",
			expectedReference:   swarm.MustParseHexAddress("a58484e3d77bbdb40323ddc9020c6e96e5eb5deb52015d3e0f63cce629ac1aa6"),
			wantIndexFilename:   "index.html",
			indexFilenameOption: jsonhttptest.WithRequestHeader(api.SwarmIndexDocumentHeader, "index.html"),
			doMultipart:         true,
			files: []f{
				{
					data: []byte("<h1>Swarm"),
					name: "index.html",
					dir:  "",
					header: http.Header{
						api.ContentTypeHeader: {"text/html; charset=utf-8"},
					},
				},
			},
		},
		{
			name:                "nested index filename",
			expectedReference:   swarm.MustParseHexAddress("3e2f008a578c435efa7a1fce146e21c4ae8c20b80fbb4c4e0c1c87ca08fef414"),
			wantIndexFilename:   "index.html",
			indexFilenameOption: jsonhttptest.WithRequestHeader(api.SwarmIndexDocumentHeader, "index.html"),
			files: []f{
				{
					data: []byte("<h1>Swarm"),
					name: "index.html",
					dir:  "dir",
					header: http.Header{
						api.ContentTypeHeader: {"text/html; charset=utf-8"},
					},
				},
			},
		},
		{
			name:                "explicit index and error filename",
			expectedReference:   swarm.MustParseHexAddress("2cd9a6ac11eefbb71b372fb97c3ef64109c409955964a294fdc183c1014b3844"),
			wantIndexFilename:   "index.html",
			wantErrorFilename:   "error.html",
			indexFilenameOption: jsonhttptest.WithRequestHeader(api.SwarmIndexDocumentHeader, "index.html"),
			errorFilenameOption: jsonhttptest.WithRequestHeader(api.SwarmErrorDocumentHeader, "error.html"),
			doMultipart:         true,
			files: []f{
				{
					data: []byte("<h1>Swarm"),
					name: "index.html",
					dir:  "",
					header: http.Header{
						api.ContentTypeHeader: {"text/html; charset=utf-8"},
					},
				},
				{
					data: []byte("<h2>404"),
					name: "error.html",
					dir:  "",
					header: http.Header{
						api.ContentTypeHeader: {"text/html; charset=utf-8"},
					},
				},
			},
		},
		{
			name:              "invalid archive paths",
			expectedReference: swarm.MustParseHexAddress("133c92414c047708f3d6a8561571a0cc96512899ff0edbd9690c857f01ab6883"),
			files: []f{
				{
					data:     []byte("<h1>Swarm"),
					name:     "index.html",
					dir:      "",
					filePath: "./index.html",
				},
				{
					data:     []byte("body {}"),
					name:     "app.css",
					dir:      "",
					filePath: "./app.css",
				},
				{
					data: []byte(`User-agent: *
		Disallow: /`),
					name:     "robots.txt",
					dir:      "",
					filePath: "./robots.txt",
				},
			},
		},
		{
			name:    "encrypted",
			encrypt: true,
			files: []f{
				{
					data:     []byte("<h1>Swarm"),
					name:     "index.html",
					dir:      "",
					filePath: "./index.html",
				},
			},
		},
	} {
		verify := func(t *testing.T, resp api.BzzUploadResponse) {
			t.Helper()
			// NOTE: reference will be different each time when encryption is enabled
			if !tc.encrypt {
				if !resp.Reference.Equal(tc.expectedReference) {
					t.Fatalf("expected root reference to match %s, got %s", tc.expectedReference, resp.Reference)
				}
			}

			// verify manifest content
			verifyManifest, err := manifest.NewDefaultManifestReference(
				resp.Reference,
				loadsave.NewReadonly(storer.ChunkStore(), storer.Cache(), redundancy.DefaultDownloadLevel),
			)
			if err != nil {
				t.Fatal(err)
			}

			validateFile := func(t *testing.T, file f, filePath string) {
				t.Helper()

				jsonhttptest.Request(t, client, http.MethodGet,
					bzzDownloadResource(resp.Reference.String(), filePath),
					http.StatusOK,
					jsonhttptest.WithExpectedResponse(file.data),
					jsonhttptest.WithRequestHeader(api.ContentTypeHeader, file.header.Get(api.ContentTypeHeader)),
				)
			}

			validateIsPermanentRedirect := func(t *testing.T, fromPath, toPath string) {
				t.Helper()

				expectedResponse := fmt.Sprintf("<a href=\"%s\">Permanent Redirect</a>.\n\n",
					bzzDownloadResource(resp.Reference.String(), toPath))

				jsonhttptest.Request(t, client, http.MethodGet,
					bzzDownloadResource(resp.Reference.String(), fromPath),
					http.StatusPermanentRedirect,
					jsonhttptest.WithExpectedResponse([]byte(expectedResponse)),
				)
			}

			validateAltPath := func(t *testing.T, fromPath, toPath string) {
				t.Helper()

				var respBytes []byte

				jsonhttptest.Request(t, client, http.MethodGet,
					bzzDownloadResource(resp.Reference.String(), toPath), http.StatusOK,
					jsonhttptest.WithPutResponseBody(&respBytes),
				)

				jsonhttptest.Request(t, client, http.MethodGet,
					bzzDownloadResource(resp.Reference.String(), fromPath), http.StatusOK,
					jsonhttptest.WithExpectedResponse(respBytes),
				)
			}

			// check if each file can be located and read
			for _, file := range tc.files {
				validateFile(t, file, path.Join(file.dir, file.name))
			}

			// check index filename
			if tc.wantIndexFilename != "" {
				entry, err := verifyManifest.Lookup(ctx, manifest.RootPath)
				if err != nil {
					t.Fatal(err)
				}

				manifestRootMetadata := entry.Metadata()
				indexDocumentSuffixPath, ok := manifestRootMetadata[manifest.WebsiteIndexDocumentSuffixKey]
				if !ok {
					t.Fatalf("expected index filename '%s', did not find any", tc.wantIndexFilename)
				}

				// check index suffix for each dir
				for _, file := range tc.files {
					if file.dir != "" {
						validateIsPermanentRedirect(t, file.dir, file.dir+"/")
						validateAltPath(t, file.dir+"/", path.Join(file.dir, indexDocumentSuffixPath))
					}
				}
			}

			// check error filename
			if tc.wantErrorFilename != "" {
				entry, err := verifyManifest.Lookup(ctx, manifest.RootPath)
				if err != nil {
					t.Fatal(err)
				}

				manifestRootMetadata := entry.Metadata()
				errorDocumentPath, ok := manifestRootMetadata[manifest.WebsiteErrorDocumentPathKey]
				if !ok {
					t.Fatalf("expected error filename '%s', did not find any", tc.wantErrorFilename)
				}

				// check error document
				validateAltPath(t, "_non_existent_file_path_", errorDocumentPath)
			}
		}
		t.Run(tc.name, func(t *testing.T) {
			t.Run("tar_upload", func(t *testing.T) {
				// tar all the test case files
				tarReader := tarFiles(t, tc.files)

				var resp api.BzzUploadResponse

				options := []jsonhttptest.Option{
					jsonhttptest.WithRequestHeader(api.SwarmDeferredUploadHeader, "true"),
					jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
					jsonhttptest.WithRequestBody(tarReader),
					jsonhttptest.WithRequestHeader(api.SwarmCollectionHeader, "True"),
					jsonhttptest.WithRequestHeader(api.ContentTypeHeader, api.ContentTypeTar),
					jsonhttptest.WithUnmarshalJSONResponse(&resp),
				}
				if tc.indexFilenameOption != nil {
					options = append(options, tc.indexFilenameOption)
				}
				if tc.errorFilenameOption != nil {
					options = append(options, tc.errorFilenameOption)
				}
				if tc.encrypt {
					options = append(options, jsonhttptest.WithRequestHeader(api.SwarmEncryptHeader, "true"))
				}

				// verify directory tar upload response
				jsonhttptest.Request(t, client, http.MethodPost, dirUploadResource, http.StatusCreated, options...)

				if resp.Reference.String() == "" {
					t.Fatalf("expected file reference, did not got any")
				}

				verify(t, resp)
			})
			if tc.doMultipart {
				t.Run("multipart_upload", func(t *testing.T) {
					// tar all the test case files
					mwReader, mwBoundary := multipartFiles(t, tc.files)

					var resp api.BzzUploadResponse

					options := []jsonhttptest.Option{
						jsonhttptest.WithRequestHeader(api.SwarmDeferredUploadHeader, "true"),
						jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
						jsonhttptest.WithRequestBody(mwReader),
						jsonhttptest.WithRequestHeader(api.SwarmCollectionHeader, "True"),
						jsonhttptest.WithRequestHeader(api.ContentTypeHeader, fmt.Sprintf("multipart/form-data; boundary=%q", mwBoundary)),
						jsonhttptest.WithUnmarshalJSONResponse(&resp),
					}
					if tc.indexFilenameOption != nil {
						options = append(options, tc.indexFilenameOption)
					}
					if tc.errorFilenameOption != nil {
						options = append(options, tc.errorFilenameOption)
					}
					if tc.encrypt {
						options = append(options, jsonhttptest.WithRequestHeader(api.SwarmEncryptHeader, "true"))
					}

					// verify directory tar upload response
					jsonhttptest.Request(t, client, http.MethodPost, dirUploadResource, http.StatusCreated, options...)

					if resp.Reference.String() == "" {
						t.Fatalf("expected file reference, did not got any")
					}

					verify(t, resp)
				})
			}
		})
	}

	t.Run("upload invalid tag", func(t *testing.T) {
		tr := tarFiles(t, []f{
			{
				data: []byte("robots text"),
				name: "robots.txt",
				dir:  "",
				header: http.Header{
					api.ContentTypeHeader: {"text/plain; charset=utf-8"},
				},
			},
		})

		jsonhttptest.Request(t, client, http.MethodPost, dirUploadResource, http.StatusBadRequest,
			jsonhttptest.WithRequestHeader(api.SwarmTagHeader, "tag"),
			jsonhttptest.WithRequestHeader(api.SwarmDeferredUploadHeader, "true"),
			jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
			jsonhttptest.WithRequestBody(tr),
			jsonhttptest.WithRequestHeader(api.ContentTypeHeader, api.ContentTypeTar),
			jsonhttptest.WithExpectedJSONResponse(jsonhttp.StatusResponse{
				Message: "invalid header params",
				Code:    http.StatusBadRequest,
				Reasons: []jsonhttp.Reason{
					{
						Field: "Swarm-Tag",
						Error: "invalid syntax",
					},
				},
			}),
		)
	})

	t.Run("upload tag not found", func(t *testing.T) {
		tr := tarFiles(t, []f{
			{
				data: []byte("robots text"),
				name: "robots.txt",
				dir:  "",
				header: http.Header{
					api.ContentTypeHeader: {"text/plain; charset=utf-8"},
				},
			},
		})

		jsonhttptest.Request(t, client, http.MethodPost, dirUploadResource, http.StatusNotFound,
			jsonhttptest.WithRequestHeader(api.SwarmTagHeader, strconv.FormatUint(uint64(10000), 10)),
			jsonhttptest.WithRequestHeader(api.SwarmDeferredUploadHeader, "true"),
			jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
			jsonhttptest.WithRequestBody(tr),
			jsonhttptest.WithRequestHeader(api.ContentTypeHeader, api.ContentTypeTar))
	})
}

func TestDirsEmtpyDir(t *testing.T) {
	t.Parallel()

	var (
		dirUploadResource = "/bzz"
		storer            = mockstorer.New()
		client, _, _, _   = newTestServer(t, testServerOptions{
			Storer:          storer,
			PreventRedirect: true,
			Post:            mockpost.New(mockpost.WithAcceptAll()),
		})
	)

	tarReader := tarEmptyDir(t)

	jsonhttptest.Request(t, client, http.MethodPost, dirUploadResource,
		http.StatusBadRequest,
		jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
		jsonhttptest.WithRequestBody(tarReader),
		jsonhttptest.WithRequestHeader(api.SwarmCollectionHeader, "true"),
		jsonhttptest.WithRequestHeader(api.ContentTypeHeader, api.ContentTypeTar),
		jsonhttptest.WithExpectedJSONResponse(jsonhttp.StatusResponse{
			Message: api.ErrEmptyDir.Error(),
			Code:    http.StatusBadRequest,
		}),
	)
}

// tarFiles receives an array of test case files and creates a new tar with those files as a collection
// it returns a bytes.Buffer which can be used to read the created tar
func tarFiles(t *testing.T, files []f) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	for _, file := range files {
		filePath := path.Join(file.dir, file.name)
		if file.filePath != "" {
			filePath = file.filePath
		}

		// create tar header and write it
		hdr := &tar.Header{
			Name: filePath,
			Mode: 0o600,
			Size: int64(len(file.data)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}

		// write the file data to the tar
		if _, err := tw.Write(file.data); err != nil {
			t.Fatal(err)
		}
	}

	// finally close the tar writer
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	return &buf
}

func tarEmptyDir(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	hdr := &tar.Header{
		Name: "empty/",
		Mode: 0o600,
	}

	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}

	// finally close the tar writer
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	return &buf
}

func multipartFiles(t *testing.T, files []f) (*bytes.Buffer, string) {
	t.Helper()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	for _, file := range files {
		filePath := path.Join(file.dir, file.name)
		if file.filePath != "" {
			filePath = file.filePath
		}

		hdr := make(textproto.MIMEHeader)
		hdr.Set(api.ContentDispositionHeader, fmt.Sprintf("form-data; name=%q", filePath))

		contentType := file.header.Get(api.ContentTypeHeader)
		if contentType != "" {
			hdr.Set(api.ContentTypeHeader, contentType)
		}
		if len(file.data) > 0 {
			hdr.Set(api.ContentLengthHeader, strconv.Itoa(len(file.data)))
		}
		part, err := mw.CreatePart(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.Copy(part, bytes.NewBuffer(file.data)); err != nil {
			t.Fatal(err)
		}
	}

	// finally close the tar writer
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	return &buf, mw.Boundary()
}

// struct for dir files for test cases
type f struct {
	data     []byte
	name     string
	dir      string
	filePath string
	header   http.Header
}

// tarShortEntry is a header promising more bytes than follow, so the body stops
// in the middle of an entry rather than before the first header.
//
// It is not the same case as a body shorter than one header block: that one
// fails in reader.Next, while this one fails while the pipeline is reading the
// entry's content, so it reaches the handler wrapped as "store dir file" rather
// than "read dir stream". Both carry io.ErrUnexpectedEOF, which is what the
// handler matches, and neither existing helper covers this path.
func tarShortEntry(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: "big.bin",
		Mode: 0o600,
		Size: 4096,
	}); err != nil {
		t.Fatal(err)
	}
	// Fewer bytes than the header promises. Flush and Close are skipped
	// only because both return "missed writing 3996 bytes" here; measured,
	// neither writes a further byte, so the body is the same 612 bytes
	// either way and omitting them just avoids a pointless error check.
	if _, err := tw.Write(bytes.Repeat([]byte("a"), 100)); err != nil {
		t.Fatal(err)
	}

	return &buf
}

// TestDirsMultipartMalformed covers the other reader. storeDir dispatches on
// the content type, so multipart bodies reach the same switch through
// multipartReader, and the spec for #409 required what that reader returns to
// be established rather than assumed.
//
// Two of these improve with the #409 fix and two do not, which is why they are
// one table: the two that do not are the same class of defect reached through
// mime/multipart's own errors rather than archive/tar's, and they are recorded
// here so the gap is visible in the tests rather than only in an issue.
func TestDirsMultipartMalformed(t *testing.T) {
	t.Parallel()

	client, _, _, _ := newTestServer(t, testServerOptions{
		Storer:          mockstorer.New(),
		PreventRedirect: true,
		Post:            mockpost.New(mockpost.WithAcceptAll()),
	})

	complete, boundary := multipartFiles(t, []f{{
		data: []byte("<h1>Swarm"),
		name: "index.html",
	}})

	for _, tc := range []struct {
		name        string
		body        []byte
		contentType string
		code        int
		message     string
	}{
		{
			// Fixed by #409: this used to answer 500.
			name:        "cut inside a part body",
			body:        complete.Bytes()[:complete.Len()-20],
			contentType: "multipart/form-data; boundary=" + boundary,
			code:        http.StatusBadRequest,
			message:     "archive ends before it is complete",
		},
		{
			// Already 400 before #409, by a different route: the reader
			// finds no parts, so the collection is empty.
			name:        "no boundary in the body",
			body:        []byte("not a multipart body at all"),
			contentType: "multipart/form-data; boundary=" + boundary,
			code:        http.StatusBadRequest,
			message:     api.ErrEmptyDir.Error(),
		},
		{
			// Fixed by #424: this used to answer 500. mime/multipart
			// refuses before reading anything, with an error it does not
			// export, so the boundary is checked before the reader is
			// built rather than matched afterwards.
			name:        "content type declares no boundary",
			body:        complete.Bytes(),
			contentType: "multipart/form-data",
			code:        http.StatusBadRequest,
			message:     api.ErrNoBoundary.Error(),
		},
		{
			// Fixed by #424: a part header line with no colon is a
			// malformed MIME header, matched by type because
			// textproto.ProtocolError carries the offending line in its
			// text and so cannot be matched by identity.
			name:        "part header line without a colon",
			body:        []byte("--" + boundary + "\r\nnot a header line\r\n\r\nbody\r\n--" + boundary + "--\r\n"),
			contentType: "multipart/form-data; boundary=" + boundary,
			code:        http.StatusBadRequest,
			message:     "malformed multipart header",
		},
		{
			// wasp #455: this used to answer 500. The message matters as
			// much as the status: the body also carries the general
			// sentinel, so a case placed above this one would answer 400
			// with "malformed multipart body" instead and a status-only
			// assertion would not notice.
			name:        "a part with more headers than mime/multipart allows",
			body:        tooManyPartHeaders(boundary),
			contentType: "multipart/form-data; boundary=" + boundary,
			code:        http.StatusBadRequest,
			message:     "multipart part headers are too large",
		},
		{
			// wasp #455: this used to answer 500, and it is the case the
			// sentinel exists for. mime/multipart builds "expecting a new
			// Part" with fmt.Errorf and the caller's own bytes, so neither
			// errors.Is nor errors.As can reach it.
			//
			// The trailing "x" is load bearing. isBoundaryDelimiterLine
			// calls skipLWSPChar, so a tab alone is stripped and the line
			// is a VALID delimiter: without a further non-whitespace
			// character this body uploads successfully and the test would
			// assert nothing.
			name:        "garbage where a new part was expected",
			body:        []byte("--" + boundary + "\r\nContent-Disposition: form-data; name=\"f\"; filename=\"i.html\"\r\n\r\nhello\r\n--" + boundary + "\tx\r\n"),
			contentType: "multipart/form-data; boundary=" + boundary,
			code:        http.StatusBadRequest,
			message:     "malformed multipart body",
		},
		{
			// The guard against the wrap swallowing the end of the parts.
			// storeDir ends its loop on errors.Is(err, io.EOF), and two %w
			// verbs keep io.EOF in the chain, so this passes either way;
			// it is a standing regression guard rather than a
			// mutation-checked test, which the spec records.
			name:        "a well formed body still succeeds",
			body:        complete.Bytes(),
			contentType: "multipart/form-data; boundary=" + boundary,
			code:        http.StatusCreated,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opts := []jsonhttptest.Option{
				jsonhttptest.WithRequestHeader(api.SwarmDeferredUploadHeader, "true"),
				jsonhttptest.WithRequestHeader(api.SwarmPostageBatchIdHeader, batchOkStr),
				jsonhttptest.WithRequestBody(bytes.NewReader(tc.body)),
				jsonhttptest.WithRequestHeader(api.SwarmCollectionHeader, "True"),
				jsonhttptest.WithRequestHeader(api.ContentTypeHeader, tc.contentType),
			}
			// A case with no message is the success one, which answers with
			// a reference rather than a status response.
			if tc.message != "" {
				opts = append(opts, jsonhttptest.WithExpectedJSONResponse(jsonhttp.StatusResponse{
					Message: tc.message,
					Code:    tc.code,
				}))
			}

			jsonhttptest.Request(t, client, http.MethodPost, "/bzz", tc.code, opts...)
		})
	}
}

// tooManyPartHeaders builds a multipart body whose single part carries more
// header lines than mime/multipart will parse, which is wasp #455.
//
// maxMIMEHeaders() returns 10000 unless the multipartmaxheaders GODEBUG says
// otherwise, and exceeding it makes readMIMEHeader report "message too large",
// which populateHeaders replaces with the exported ErrMessageTooLarge. The
// limit is on one part's headers rather than on the upload, so no well-formed
// upload reaches it however large it is.
func tooManyPartHeaders(boundary string) []byte {
	var b strings.Builder

	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Disposition: form-data; name=\"f\"; filename=\"index.html\"\r\n")
	for i := 0; i < 10001; i++ {
		fmt.Fprintf(&b, "X-Wasp-%d: v\r\n", i)
	}
	b.WriteString("\r\n<h1>Swarm\r\n--" + boundary + "--\r\n")

	return []byte(b.String())
}

// tarShorterThanHeader is the nine byte body the pre-#409 test used, kept so
// the behaviour change is visible on exactly the input that asserted the 500.
// archive/tar reports anything shorter than one 512-byte header block as an
// unexpected end of input.
//
// A named function rather than a closure, for the reason localingest_dir_test.go
// records next to tarEmpty: every case in a table should be one.
func tarShorterThanHeader(t *testing.T) *bytes.Buffer {
	t.Helper()

	return bytes.NewBufferString("some data")
}
