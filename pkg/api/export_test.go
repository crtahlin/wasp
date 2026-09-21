// Copyright 2020 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api

import (
	"context"

	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/ethersphere/bee/v2/pkg/retrieval"
	"github.com/ethersphere/bee/v2/pkg/storage"
	"github.com/ethersphere/bee/v2/pkg/swarm"
)

type (
	LocalIngestResponse     = localIngestResponse
	LocalIngestFullResponse = localIngestFullResponse

	BytesPostResponse     = bytesPostResponse
	ChunkAddressResponse  = chunkAddressResponse
	SocPostResponse       = socPostResponse
	FeedReferenceResponse = feedReferenceResponse
	BzzUploadResponse     = bzzUploadResponse
	TagRequest            = tagRequest
	ListTagsResponse      = listTagsResponse
	IsRetrievableResponse = isRetrievableResponse
)

var (
	ErrInvalidContentType   = errInvalidContentType
	ErrInvalidRequest       = errInvalidRequest
	ErrDirectoryStoreError  = errDirectoryStore
	ErrEmptyDir             = errEmptyDir
	ErrInvalidIndexDocument = errInvalidIndexDocument
)

var ContentTypeTar = contentTypeTar

var (
	ErrNoResolver                       = errNoResolver
	ErrInvalidNameOrAddress             = errInvalidNameOrAddress
	ErrOperationSupportedOnlyInFullMode = errOperationSupportedOnlyInFullMode
	ErrActDownload                      = errActDownload
	ErrActUpload                        = errActUpload
)

var (
	FeedMetadataEntryOwner = feedMetadataEntryOwner
	FeedMetadataEntryTopic = feedMetadataEntryTopic
	FeedMetadataEntryType  = feedMetadataEntryType

	SuccessWsMsg = successWsMsg
)

var (
	FileSizeBucketsKBytes = fileSizeBucketsKBytes
	ToFileSizeBucket      = toFileSizeBucket
)

func (s *Service) ResolveNameOrAddress(str string) (swarm.Address, error) {
	return s.resolveNameOrAddress(str)
}

type (
	HealthStatusResponse              = healthStatusResponse
	NodeResponse                      = nodeResponse
	PingpongResponse                  = pingpongResponse
	PeerConnectResponse               = peerConnectResponse
	PeersResponse                     = peersResponse
	BlockedListedPeersResponse        = blockListedPeersResponse
	AddressesResponse                 = addressesResponse
	WelcomeMessageRequest             = welcomeMessageRequest
	WelcomeMessageResponse            = welcomeMessageResponse
	BalancesResponse                  = balancesResponse
	PeerDataResponse                  = peerDataResponse
	PeerData                          = peerData
	BalanceResponse                   = balanceResponse
	SettlementResponse                = settlementResponse
	SettlementsResponse               = settlementsResponse
	ChequebookBalanceResponse         = chequebookBalanceResponse
	ChequebookAddressResponse         = chequebookAddressResponse
	ChequebookLastChequePeerResponse  = chequebookLastChequePeerResponse
	ChequebookLastChequesResponse     = chequebookLastChequesResponse
	ChequebookLastChequesPeerResponse = chequebookLastChequesPeerResponse
	ChequebookTxResponse              = chequebookTxResponse
	SwapCashoutResponse               = swapCashoutResponse
	SwapCashoutStatusResponse         = swapCashoutStatusResponse
	SwapCashoutStatusResult           = swapCashoutStatusResult
	TransactionInfo                   = transactionInfo
	TransactionPendingList            = transactionPendingList
	TransactionHashResponse           = transactionHashResponse
	TagResponse                       = tagResponse
	ReserveStateResponse              = reserveStateResponse
	ChainStateResponse                = chainStateResponse
	PostageCreateResponse             = postageCreateResponse
	PostageStampResponse              = postageStampResponse
	PostageStampsResponse             = postageStampsResponse
	PostageBatchResponse              = postageBatchResponse
	PostageStampBucketsResponse       = postageStampBucketsResponse
	BucketData                        = bucketData
	WalletResponse                    = walletResponse
	WalletTxResponse                  = walletTxResponse
	GetStakeResponse                  = getStakeResponse
	GetWithdrawableResponse           = getWithdrawableResponse
	LegacyStakeResponse               = legacyStakeResponse
	LegacyStakeEntryResponse          = legacyStakeEntryResponse
	LegacyRecoverResponse             = legacyRecoverResponse
	LegacyRecoverAllResponse          = legacyRecoverAllResponse
	LegacyStatusResponse              = legacyStatusResponse
	StakeTransactionReponse           = stakeTransactionReponse
	StatusSnapshotResponse            = statusSnapshotResponse
	StatusResponse                    = statusResponse
)

var (
	ErrCantBalance           = errCantBalance
	ErrCantBalances          = errCantBalances
	HttpErrGetAccountingInfo = httpErrGetAccountingInfo
	ErrNoBalance             = errNoBalance
	ErrCantSettlementsPeer   = errCantSettlementsPeer
	ErrCantSettlements       = errCantSettlements
	ErrChequebookBalance     = errChequebookBalance
	ErrInvalidAddress        = errInvalidAddress
	ErrUnknownTransaction    = errUnknownTransaction
	ErrCantGetTransaction    = errCantGetTransaction
	ErrCantResendTransaction = errCantResendTransaction
	ErrAlreadyImported       = errAlreadyImported
)

type (
	LogRegistryIterateFn   func(fn func(string, string, log.Level, uint) bool)
	LogSetVerbosityByExpFn func(e string, v log.Level) error
)

var (
	LogRegistryIterate   = logRegistryIterate
	LogSetVerbosityByExp = logSetVerbosityByExp
)

func ReplaceLogRegistryIterateFn(fn LogRegistryIterateFn)   { logRegistryIterate = fn }
func ReplaceLogSetVerbosityByExp(fn LogSetVerbosityByExpFn) { logSetVerbosityByExp = fn }

var ErrHexLength = errHexLength

type HexInvalidByteError = hexInvalidByteError

func MapStructure(input, output any, hooks map[string]func(v string) (string, error)) error {
	return mapStructure(input, output, hooks)
}

func NewParseError(entry, value string, cause error) error {
	return newParseError(entry, value, cause)
}

// NewProviderGetterForTest builds the hint withProviders would build and
// returns providerGetter's wrapper, so a test can assert on the context a fetch
// arrives with. The wrapper is where issue #299's re-attach happens.
//
// The outer context is deliberately bare. withProviders also attaches the set
// to it and calls ConnectHints; neither is reproduced, because providerGetter
// reads only the hint from that context, and a test that mimicked the rest
// would be asserting on withProviders rather than on the wrapper.
//
// It constructs a bare Service because providerGetter returns the getter
// unwrapped when s.providers is nil, and newTestServer does not hand back the
// Service.
func NewProviderGetterForTest(p Providers, key []byte, set *retrieval.PreferredSet, g storage.Getter) storage.Getter {
	s := &Service{providers: p}
	hint := &providerHint{set: set, key: key}
	ctx := context.WithValue(context.Background(), providerHintKey{}, hint)
	return s.providerGetter(ctx, g)
}
