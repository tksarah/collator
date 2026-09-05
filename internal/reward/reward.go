package reward

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/blake2b"

	"github.com/shiden-guardian/shiden-guardian/internal/model"
)

const (
	ShidenPrefix        = byte(5)
	CurrentSpecVersion  = int64(2400)
	AccountInfoBytes    = 80
	ExistentialDeposit  = int64(1_000_000)
	KickThresholdBlocks = int64(1200)
	maxRPCBody          = 4 << 20
)

// IsSupportedSpecVersion deliberately lists every runtime whose storage
// layout has been verified. Unknown runtimes remain fail-closed until their
// AccountInfo and collator-selection layout are reviewed.
func IsSupportedSpecVersion(version int64) bool {
	return version == 2208 || version == 2300 || version == CurrentSpecVersion
}

var b58Alphabet = []byte("123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz")

type Keys struct {
	AccountID     [32]byte
	WalletAccount string
	PotAccount    string
	LastAuthored  string
	Validators    string
	TimestampNow  string
}

func StorageKeys(address string) (Keys, error) {
	account, prefix, err := DecodeSS58(address)
	if err != nil {
		return Keys{}, err
	}
	if prefix != ShidenPrefix {
		return Keys{}, fmt.Errorf("unexpected SS58 prefix %d", prefix)
	}
	pot := [32]byte{}
	copy(pot[:], []byte("modlPotStake"))
	return Keys{
		AccountID:     account,
		WalletAccount: accountStorageKey(account),
		PotAccount:    accountStorageKey(pot),
		LastAuthored:  storageMapKey("CollatorSelection", "LastAuthoredBlock", twox64Concat(account[:])),
		Validators:    storagePlainKey("Session", "Validators"),
		TimestampNow:  storagePlainKey("Timestamp", "Now"),
	}, nil
}

func DecodeSS58(address string) ([32]byte, byte, error) {
	var account [32]byte
	raw, err := decodeBase58(address)
	if err != nil || len(raw) != 35 {
		return account, 0, fmt.Errorf("invalid SS58 address")
	}
	if raw[0] >= 64 {
		return account, 0, fmt.Errorf("two-byte SS58 prefixes are not supported")
	}
	payload := raw[:33]
	checksum := blake2b.Sum512(append([]byte("SS58PRE"), payload...))
	if subtle.ConstantTimeCompare(raw[33:], checksum[:2]) != 1 {
		return account, 0, fmt.Errorf("invalid SS58 checksum")
	}
	copy(account[:], raw[1:33])
	return account, raw[0], nil
}

func decodeBase58(value string) ([]byte, error) {
	n := new(big.Int)
	base := big.NewInt(58)
	for i := 0; i < len(value); i++ {
		idx := bytes.IndexByte(b58Alphabet, value[i])
		if idx < 0 {
			return nil, errors.New("invalid base58 character")
		}
		n.Mul(n, base)
		n.Add(n, big.NewInt(int64(idx)))
	}
	out := n.Bytes()
	for i := 0; i < len(value) && value[i] == '1'; i++ {
		out = append([]byte{0}, out...)
	}
	return out, nil
}

func storagePlainKey(pallet, item string) string {
	return "0x" + hex.EncodeToString(append(twox128([]byte(pallet)), twox128([]byte(item))...))
}
func storageMapKey(pallet, item string, suffix []byte) string {
	key := append(twox128([]byte(pallet)), twox128([]byte(item))...)
	key = append(key, suffix...)
	return "0x" + hex.EncodeToString(key)
}
func accountStorageKey(account [32]byte) string {
	digest, _ := blake2b.New(16, nil)
	_, _ = digest.Write(account[:])
	return storageMapKey("System", "Account", append(digest.Sum(nil), account[:]...))
}
func twox64Concat(value []byte) []byte {
	out := make([]byte, 8, 8+len(value))
	binary.LittleEndian.PutUint64(out, xxhash64(value, 0))
	return append(out, value...)
}
func twox128(value []byte) []byte {
	out := make([]byte, 16)
	binary.LittleEndian.PutUint64(out[:8], xxhash64(value, 0))
	binary.LittleEndian.PutUint64(out[8:], xxhash64(value, 1))
	return out
}

type rpcCaller interface {
	Call(context.Context, string, []any, any) error
}

type HTTPRPC struct {
	URL    string
	Client *http.Client
}

func NewHTTPRPC(url string) *HTTPRPC {
	return &HTTPRPC{URL: url, Client: &http.Client{Timeout: 10 * time.Second}}
}
func (c *HTTPRPC) Call(ctx context.Context, method string, params []any, out any) error {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.Client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxRPCBody))
	if err != nil {
		return err
	}
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("rpc http %s", response.Status)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	if envelope.Error != nil {
		return fmt.Errorf("rpc %d: %s", envelope.Error.Code, envelope.Error.Message)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Result, out)
}

type Monitor struct {
	Address string
	Source  string
	Keys    Keys
	RPC     rpcCaller
}

func NewMonitor(address, source string, rpc rpcCaller) (*Monitor, error) {
	keys, err := StorageKeys(address)
	if err != nil {
		return nil, err
	}
	return &Monitor{Address: address, Source: source, Keys: keys, RPC: rpc}, nil
}

func (m *Monitor) Snapshot(ctx context.Context) (model.RewardSnapshot, error) {
	out := model.RewardSnapshot{Address: m.Address, Source: m.Source, ObservedAt: time.Now().UTC()}
	var head string
	if err := m.RPC.Call(ctx, "chain_getFinalizedHead", []any{}, &head); err != nil {
		return out, err
	}
	out.FinalizedHash = head
	header, err := m.header(ctx, head)
	if err != nil {
		return out, err
	}
	out.FinalizedBlock = header
	var version struct {
		SpecVersion json.Number `json:"specVersion"`
	}
	if err := m.RPC.Call(ctx, "state_getRuntimeVersion", []any{head}, &version); err != nil {
		return out, err
	}
	out.SpecVersion, _ = strconv.ParseInt(string(version.SpecVersion), 10, 64)
	accountRaw, err := m.storage(ctx, m.Keys.WalletAccount, head)
	if err != nil {
		return out, err
	}
	free, reserved, schemaOK, err := DecodeAccountInfo(accountRaw)
	if err != nil {
		return out, err
	}
	out.WalletFreePlanck, out.WalletReservedPlanck = free.String(), reserved.String()
	out.SchemaOK = schemaOK && IsSupportedSpecVersion(out.SpecVersion)
	lastRaw, err := m.storage(ctx, m.Keys.LastAuthored, head)
	if err != nil {
		return out, err
	}
	out.LastAuthoredBlock, err = DecodeBlockNumber(lastRaw)
	if err != nil {
		return out, err
	}
	validatorsRaw, err := m.storage(ctx, m.Keys.Validators, head)
	if err != nil {
		return out, err
	}
	validators, err := DecodeAccountVector(validatorsRaw)
	if err != nil {
		return out, err
	}
	out.ValidatorCount = len(validators)
	for _, account := range validators {
		if account == m.Keys.AccountID {
			out.ActiveSession = true
			break
		}
	}
	if out.LastAuthoredBlock > 0 {
		if hash, hashErr := m.blockHash(ctx, out.LastAuthoredBlock); hashErr == nil {
			if timestampRaw, timestampErr := m.storage(ctx, m.Keys.TimestampNow, hash); timestampErr == nil && len(timestampRaw) == 8 {
				out.LastAuthoredAt = time.UnixMilli(int64(binary.LittleEndian.Uint64(timestampRaw))).UTC()
			}
		}
	}
	if !out.SchemaOK {
		out.Error = "unsupported_runtime_schema"
	}
	return out, nil
}

func (m *Monitor) Scan(ctx context.Context, from, to int64) (model.RewardScan, error) {
	out := model.RewardScan{From: from, To: to, Source: m.Source, Observations: []model.RewardObservation{}}
	if from <= 0 || to < from || to-from+1 > 128 {
		return out, fmt.Errorf("invalid scan range")
	}
	previousHash, err := m.blockHash(ctx, from-1)
	if err != nil {
		return out, err
	}
	endHash, err := m.blockHash(ctx, to)
	if err != nil {
		return out, err
	}
	for _, hash := range []string{previousHash, endHash} {
		version, versionErr := m.runtimeVersion(ctx, hash)
		if versionErr != nil {
			return out, versionErr
		}
		if !IsSupportedSpecVersion(version) {
			return out, fmt.Errorf("unsupported runtime spec %d", version)
		}
	}
	previousRaw, err := m.storage(ctx, m.Keys.LastAuthored, previousHash)
	if err != nil {
		return out, err
	}
	previous, err := DecodeBlockNumber(previousRaw)
	if err != nil {
		return out, err
	}
	for block := from; block <= to; block++ {
		hash, err := m.blockHash(ctx, block)
		if err != nil {
			return out, err
		}
		lastRaw, err := m.storage(ctx, m.Keys.LastAuthored, hash)
		if err != nil {
			return out, err
		}
		last, err := DecodeBlockNumber(lastRaw)
		if err != nil {
			return out, err
		}
		if last == block && last != previous {
			observation, err := m.observeReward(ctx, block, hash, previousHash)
			if err != nil {
				return out, err
			}
			out.Observations = append(out.Observations, observation)
		}
		previous, previousHash = last, hash
	}
	out.LastAuthored = previous
	return out, nil
}

func (m *Monitor) observeReward(ctx context.Context, block int64, hash, parentHash string) (model.RewardObservation, error) {
	out := model.RewardObservation{BlockNumber: block, BlockHash: hash, Verification: "unknown", SourceCount: 1, Evidence: map[string]any{"source": m.Source}}
	walletBeforeRaw, err := m.storage(ctx, m.Keys.WalletAccount, parentHash)
	if err != nil {
		return out, err
	}
	walletAfterRaw, err := m.storage(ctx, m.Keys.WalletAccount, hash)
	if err != nil {
		return out, err
	}
	potRaw, err := m.storage(ctx, m.Keys.PotAccount, parentHash)
	if err != nil {
		return out, err
	}
	walletBefore, _, ok1, err := DecodeAccountInfo(walletBeforeRaw)
	if err != nil {
		return out, err
	}
	walletAfter, _, ok2, err := DecodeAccountInfo(walletAfterRaw)
	if err != nil {
		return out, err
	}
	pot, _, ok3, err := DecodeAccountInfo(potRaw)
	if err != nil {
		return out, err
	}
	if !ok1 || !ok2 || !ok3 {
		return out, fmt.Errorf("unsupported account info")
	}
	expected := ExpectedReward(pot)
	credited := new(big.Int).Sub(walletAfter, walletBefore)
	if credited.Sign() < 0 {
		credited.SetInt64(0)
	}
	out.ExpectedPlanck, out.CreditedPlanck = expected.String(), credited.String()
	out.WalletBeforePlanck, out.WalletAfterPlanck, out.PotBeforePlanck = walletBefore.String(), walletAfter.String(), pot.String()
	switch {
	case expected.Sign() == 0:
		out.Verification = "pot_empty"
	case credited.Cmp(expected) < 0:
		out.Verification = "insufficient"
	case credited.Cmp(expected) > 0:
		out.Verification = "confirmed_extra"
	default:
		out.Verification = "confirmed"
	}
	if timestampRaw, timestampErr := m.storage(ctx, m.Keys.TimestampNow, hash); timestampErr == nil && len(timestampRaw) == 8 {
		out.AuthoredAt = time.UnixMilli(int64(binary.LittleEndian.Uint64(timestampRaw))).UTC()
	}
	return out, nil
}

func (m *Monitor) blockHash(ctx context.Context, block int64) (string, error) {
	var hash string
	if err := m.RPC.Call(ctx, "chain_getBlockHash", []any{block}, &hash); err != nil {
		return "", err
	}
	if hash == "" {
		return "", fmt.Errorf("block hash unavailable")
	}
	return hash, nil
}
func (m *Monitor) header(ctx context.Context, hash string) (int64, error) {
	var header struct {
		Number string `json:"number"`
	}
	if err := m.RPC.Call(ctx, "chain_getHeader", []any{hash}, &header); err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimPrefix(header.Number, "0x"), 16, 64)
}

func (m *Monitor) runtimeVersion(ctx context.Context, hash string) (int64, error) {
	var version struct {
		SpecVersion json.Number `json:"specVersion"`
	}
	if err := m.RPC.Call(ctx, "state_getRuntimeVersion", []any{hash}, &version); err != nil {
		return 0, err
	}
	value, err := strconv.ParseInt(string(version.SpecVersion), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid runtime spec: %w", err)
	}
	return value, nil
}
func (m *Monitor) storage(ctx context.Context, key, hash string) ([]byte, error) {
	var value *string
	if err := m.RPC.Call(ctx, "state_getStorage", []any{key, hash}, &value); err != nil {
		return nil, err
	}
	if value == nil || *value == "" {
		return nil, fmt.Errorf("storage unavailable for %s", key[:min(len(key), 18)])
	}
	return hex.DecodeString(strings.TrimPrefix(*value, "0x"))
}

func DecodeAccountInfo(raw []byte) (free, reserved *big.Int, schemaOK bool, err error) {
	if len(raw) < 48 {
		return nil, nil, false, fmt.Errorf("account info too short: %d", len(raw))
	}
	free = littleInt(raw[16:32])
	reserved = littleInt(raw[32:48])
	return free, reserved, len(raw) == AccountInfoBytes, nil
}
func DecodeBlockNumber(raw []byte) (int64, error) {
	switch len(raw) {
	case 4:
		return int64(binary.LittleEndian.Uint32(raw)), nil
	case 8:
		return int64(binary.LittleEndian.Uint64(raw)), nil
	default:
		return 0, fmt.Errorf("unsupported block number length %d", len(raw))
	}
}
func DecodeAccountVector(raw []byte) ([][32]byte, error) {
	count, offset, err := decodeCompact(raw)
	if err != nil {
		return nil, err
	}
	if count > 1024 || offset+count*32 != len(raw) {
		return nil, fmt.Errorf("invalid account vector")
	}
	out := make([][32]byte, count)
	for i := 0; i < count; i++ {
		copy(out[i][:], raw[offset+i*32:offset+(i+1)*32])
	}
	return out, nil
}
func decodeCompact(raw []byte) (int, int, error) {
	if len(raw) == 0 {
		return 0, 0, errors.New("empty compact")
	}
	mode := raw[0] & 3
	switch mode {
	case 0:
		return int(raw[0] >> 2), 1, nil
	case 1:
		if len(raw) < 2 {
			return 0, 0, errors.New("short compact")
		}
		return int(binary.LittleEndian.Uint16(raw[:2]) >> 2), 2, nil
	case 2:
		if len(raw) < 4 {
			return 0, 0, errors.New("short compact")
		}
		return int(binary.LittleEndian.Uint32(raw[:4]) >> 2), 4, nil
	default:
		return 0, 0, errors.New("large compact vectors are not supported")
	}
}
func littleInt(raw []byte) *big.Int {
	copied := append([]byte(nil), raw...)
	for i, j := 0, len(copied)-1; i < j; i, j = i+1, j-1 {
		copied[i], copied[j] = copied[j], copied[i]
	}
	return new(big.Int).SetBytes(copied)
}
func ExpectedReward(potFree *big.Int) *big.Int {
	value := new(big.Int).Sub(new(big.Int).Set(potFree), big.NewInt(ExistentialDeposit))
	if value.Sign() <= 0 {
		return new(big.Int)
	}
	return value.Div(value, big.NewInt(2))
}

// xxhash64 implements the xxHash64 variant used by Substrate Twox hashers.
func xxhash64(input []byte, seed uint64) uint64 {
	const p1 uint64 = 11400714785074694791
	const p2 uint64 = 14029467366897019727
	const p3 uint64 = 1609587929392839161
	const p4 uint64 = 9650029242287828579
	const p5 uint64 = 2870177450012600261
	rot := func(x uint64, r int) uint64 { return x<<r | x>>(64-r) }
	round := func(acc, lane uint64) uint64 { acc += lane * p2; acc = rot(acc, 31); return acc * p1 }
	i := 0
	var h uint64
	if len(input) >= 32 {
		v1 := seed + p1 + p2
		v2 := seed + p2
		v3 := seed
		v4 := seed - p1
		for i <= len(input)-32 {
			v1 = round(v1, binary.LittleEndian.Uint64(input[i:]))
			v2 = round(v2, binary.LittleEndian.Uint64(input[i+8:]))
			v3 = round(v3, binary.LittleEndian.Uint64(input[i+16:]))
			v4 = round(v4, binary.LittleEndian.Uint64(input[i+24:]))
			i += 32
		}
		h = rot(v1, 1) + rot(v2, 7) + rot(v3, 12) + rot(v4, 18)
		for _, v := range []uint64{v1, v2, v3, v4} {
			v = round(0, v)
			h ^= v
			h = h*p1 + p4
		}
	} else {
		h = seed + p5
	}
	h += uint64(len(input))
	for i <= len(input)-8 {
		k := round(0, binary.LittleEndian.Uint64(input[i:]))
		h ^= k
		h = rot(h, 27)*p1 + p4
		i += 8
	}
	if i <= len(input)-4 {
		h ^= uint64(binary.LittleEndian.Uint32(input[i:])) * p1
		h = rot(h, 23)*p2 + p3
		i += 4
	}
	for i < len(input) {
		h ^= uint64(input[i]) * p5
		h = rot(h, 11) * p1
		i++
	}
	h ^= h >> 33
	h *= p2
	h ^= h >> 29
	h *= p3
	h ^= h >> 32
	return h
}
