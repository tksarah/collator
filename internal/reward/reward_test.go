package reward

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"testing"
)

func TestDecodeShidenRewardAddress(t *testing.T) {
	account, prefix, err := DecodeSS58("WGYDjFY3JSijqBMkKEv7qfWU6XaRnmzigQG7B6G1zh7jBzN")
	if err != nil {
		t.Fatal(err)
	}
	if prefix != 5 {
		t.Fatalf("prefix=%d", prefix)
	}
	if got := strings.ToLower(hexString(account[:])); got != "0eac8b9620fd3552ff3e7ac7244b0369d2bf1325e2db75963f916a407f026f00" {
		t.Fatalf("account=%s", got)
	}
	if _, _, err := DecodeSS58("WGYDjFY3JSijqBMkKEv7qfWU6XaRnmzigQG7B6G1zh7jBzM"); err == nil {
		t.Fatal("bad checksum accepted")
	}
}

func TestSubstrateStorageKeysAreStable(t *testing.T) {
	keys, err := StorageKeys("WGYDjFY3JSijqBMkKEv7qfWU6XaRnmzigQG7B6G1zh7jBzN")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(keys.WalletAccount, "0x26aa394eea5630e07c48ae0c9558cef7") {
		t.Fatalf("system account key=%s", keys.WalletAccount)
	}
	if len(keys.LastAuthored) != 2+(16+16+8+32)*2 {
		t.Fatalf("last authored key length=%d", len(keys.LastAuthored))
	}
	if got := xxhash64(nil, 0); got != 0xef46db3751d8e999 {
		t.Fatalf("xxhash=%x", got)
	}
}

func TestDecodeAccountInfoAndRewardFloor(t *testing.T) {
	raw := make([]byte, AccountInfoBytes)
	putU128(raw[16:32], new(big.Int).SetUint64(6_278_626_775))
	putU128(raw[32:48], new(big.Int).SetUint64(1_070_000))
	free, reserved, ok, err := DecodeAccountInfo(raw)
	if err != nil || !ok || free.String() != "6278626775" || reserved.String() != "1070000" {
		t.Fatalf("free=%v reserved=%v ok=%v err=%v", free, reserved, ok, err)
	}
	if got := ExpectedReward(big.NewInt(1_000_005)); got.String() != "2" {
		t.Fatalf("floor reward=%s", got)
	}
	if got := ExpectedReward(big.NewInt(999_999)); got.Sign() != 0 {
		t.Fatalf("empty pot=%s", got)
	}
}

func TestDecodeValidatorVector(t *testing.T) {
	raw := make([]byte, 1+12*32)
	raw[0] = 12 << 2
	for i := 0; i < 12; i++ {
		raw[1+i*32] = byte(i + 1)
	}
	items, err := DecodeAccountVector(raw)
	if err != nil || len(items) != 12 || items[11][0] != 12 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
	if _, err := DecodeAccountVector(append(raw, 0)); err == nil {
		t.Fatal("trailing data accepted")
	}
}

func TestDecodeBlockNumber(t *testing.T) {
	raw := make([]byte, 4)
	binary.LittleEndian.PutUint32(raw, 15_682_955)
	value, err := DecodeBlockNumber(raw)
	if err != nil || value != 15_682_955 {
		t.Fatalf("value=%d err=%v", value, err)
	}
}

func TestSupportedRuntimeVersionsFailClosed(t *testing.T) {
	for _, version := range []int64{2208, 2300, 2400} {
		if !IsSupportedSpecVersion(version) {
			t.Fatalf("verified runtime %d rejected", version)
		}
	}
	if IsSupportedSpecVersion(CurrentSpecVersion + 1) {
		t.Fatal("unknown runtime accepted")
	}
}

func TestScanRejectsUnsafeRangeBeforeRPC(t *testing.T) {
	monitor, err := NewMonitor("WGYDjFY3JSijqBMkKEv7qfWU6XaRnmzigQG7B6G1zh7jBzN", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := monitor.Scan(t.Context(), 1, 129); err == nil {
		t.Fatal("range over 128 accepted")
	}
	if _, err := monitor.Scan(t.Context(), 10, 9); err == nil {
		t.Fatal("reverse range accepted")
	}
}

func TestScanVerifiesRewardFromHistoricalState(t *testing.T) {
	tests := []struct {
		name, after, pot, verification, credited, expected string
	}{
		{"confirmed", "1241", "1000482", "confirmed", "241", "241"},
		{"extra deposit", "1300", "1000482", "confirmed_extra", "300", "241"},
		{"insufficient", "1200", "1000482", "insufficient", "200", "241"},
		{"empty pot", "1000", "1000000", "pot_empty", "0", "0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rpc := &scanRPC{runtimeVersion: CurrentSpecVersion, walletBefore: big.NewInt(1000), walletAfter: mustBig(test.after), pot: mustBig(test.pot)}
			monitor, err := NewMonitor("WGYDjFY3JSijqBMkKEv7qfWU6XaRnmzigQG7B6G1zh7jBzN", "fake", rpc)
			if err != nil {
				t.Fatal(err)
			}
			rpc.keys = monitor.Keys
			scan, err := monitor.Scan(t.Context(), 100, 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(scan.Observations) != 1 {
				t.Fatalf("observations=%d", len(scan.Observations))
			}
			got := scan.Observations[0]
			if got.Verification != test.verification || got.CreditedPlanck != test.credited || got.ExpectedPlanck != test.expected || got.SourceCount != 1 {
				t.Fatalf("observation=%#v", got)
			}
		})
	}
}

func TestScanStopsOnUnknownRuntime(t *testing.T) {
	rpc := &scanRPC{runtimeVersion: CurrentSpecVersion + 1, walletBefore: big.NewInt(1000), walletAfter: big.NewInt(1241), pot: big.NewInt(1_000_482)}
	monitor, err := NewMonitor("WGYDjFY3JSijqBMkKEv7qfWU6XaRnmzigQG7B6G1zh7jBzN", "fake", rpc)
	if err != nil {
		t.Fatal(err)
	}
	rpc.keys = monitor.Keys
	if _, err := monitor.Scan(t.Context(), 100, 100); err == nil || !strings.Contains(err.Error(), "unsupported runtime") {
		t.Fatalf("unknown runtime error=%v", err)
	}
}

func TestRuntimeSnapshotAndScanCompatibility(t *testing.T) {
	for _, version := range []int64{2208, 2300, 2400, 2401} {
		for _, size := range []int{80, 64, 32} {
			t.Run(fmt.Sprintf("spec_%d_bytes_%d", version, size), func(t *testing.T) {
				rpc := &scanRPC{runtimeVersion: version, accountBytes: size, walletBefore: big.NewInt(1000), walletAfter: big.NewInt(1241), pot: big.NewInt(1_000_482)}
				monitor, err := NewMonitor("WGYDjFY3JSijqBMkKEv7qfWU6XaRnmzigQG7B6G1zh7jBzN", "local", rpc)
				if err != nil {
					t.Fatal(err)
				}
				rpc.keys = monitor.Keys
				snapshot, snapshotErr := monitor.Snapshot(t.Context())
				valid := version != 2401 && size == 80
				if valid {
					if snapshotErr != nil || !snapshot.SchemaOK || snapshot.SpecVersion != version || snapshot.Source != "local" || snapshot.FinalizedBlock != 100 || snapshot.FinalizedHash == "" || !snapshot.ActiveSession || snapshot.WalletFreePlanck != "1241" {
						t.Fatalf("snapshot=%#v err=%v", snapshot, snapshotErr)
					}
				} else if snapshotErr == nil && (snapshot.SchemaOK || snapshot.Error != "unsupported_runtime_schema") {
					t.Fatalf("unsafe snapshot=%#v", snapshot)
				}
				scan, scanErr := monitor.Scan(t.Context(), 100, 100)
				if valid {
					if scanErr != nil || len(scan.Observations) != 1 || scan.Observations[0].Verification != "confirmed" || scan.Observations[0].ExpectedPlanck != "241" {
						t.Fatalf("scan=%#v err=%v", scan, scanErr)
					}
				} else if scanErr == nil {
					t.Fatal("unsafe scan accepted")
				}
			})
		}
	}
}

type scanRPC struct {
	keys                           Keys
	runtimeVersion                 int64
	walletBefore, walletAfter, pot *big.Int
	accountBytes                   int
}

func (f *scanRPC) Call(_ context.Context, method string, params []any, out any) error {
	hash99 := fmt.Sprintf("0x%064x", 99)
	hash100 := fmt.Sprintf("0x%064x", 100)
	switch method {
	case "chain_getFinalizedHead":
		return assignJSON(out, hash100)
	case "chain_getHeader":
		return assignJSON(out, map[string]any{"number": "0x64"})
	case "chain_getBlockHash":
		block, ok := params[0].(int64)
		if !ok {
			return fmt.Errorf("unexpected block type %T", params[0])
		}
		return assignJSON(out, fmt.Sprintf("0x%064x", block))
	case "state_getRuntimeVersion":
		return assignJSON(out, map[string]any{"specVersion": f.runtimeVersion})
	case "state_getStorage":
		key, _ := params[0].(string)
		hash, _ := params[1].(string)
		var raw []byte
		switch {
		case key == f.keys.Validators:
			raw = append([]byte{4}, f.keys.AccountID[:]...)
		case key == f.keys.LastAuthored && hash == hash99:
			raw = make([]byte, 4)
			binary.LittleEndian.PutUint32(raw, 90)
		case key == f.keys.LastAuthored && hash == hash100:
			raw = make([]byte, 4)
			binary.LittleEndian.PutUint32(raw, 100)
		case key == f.keys.WalletAccount && hash == hash99:
			raw = accountInfo(f.walletBefore)
		case key == f.keys.WalletAccount && hash == hash100:
			raw = accountInfo(f.walletAfter)
		case key == f.keys.PotAccount && hash == hash99:
			raw = accountInfo(f.pot)
		case key == f.keys.TimestampNow && hash == hash100:
			raw = make([]byte, 8)
			binary.LittleEndian.PutUint64(raw, 1_722_816_000_000)
		default:
			return fmt.Errorf("unexpected storage key=%s hash=%s", key, hash)
		}
		if key == f.keys.WalletAccount && f.accountBytes > 0 {
			raw = raw[:f.accountBytes]
		}
		return assignJSON(out, "0x"+hex.EncodeToString(raw))
	default:
		return fmt.Errorf("unexpected method %s", method)
	}
}

func assignJSON(out, value any) error {
	raw, _ := json.Marshal(value)
	return json.Unmarshal(raw, out)
}

func accountInfo(free *big.Int) []byte {
	raw := make([]byte, AccountInfoBytes)
	putU128(raw[16:32], free)
	return raw
}

func mustBig(value string) *big.Int {
	out, ok := new(big.Int).SetString(value, 10)
	if !ok {
		panic(value)
	}
	return out
}

func hexString(raw []byte) string {
	const alphabet = "0123456789abcdef"
	out := make([]byte, len(raw)*2)
	for i, b := range raw {
		out[i*2] = alphabet[b>>4]
		out[i*2+1] = alphabet[b&15]
	}
	return string(out)
}
func putU128(dst []byte, value *big.Int) {
	raw := value.Bytes()
	for i := range raw {
		dst[i] = raw[len(raw)-1-i]
	}
}
