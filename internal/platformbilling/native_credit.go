package platformbilling

import (
	"github.com/QuantumNous/new-api/common"
	"math/big"
	"reflect"
	"regexp"
)

const NativeCreditPricingSource = "llmrouter_native_credit"
const NativeCreditPricingVersion = "lr-native-credit-v1"

var nativeCreditVersion = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

func (c NativeConversion) validCredit() bool {
	if !nativeCreditVersion.MatchString(c.Version) || c.Version == "lr-quota-usd-cny-v1" || c.Rounding != "lr-cumulative-ceil-v1" {
		return false
	}
	n, nOK := new(big.Int).SetString(c.Numerator, 10)
	d, dOK := new(big.Int).SetString(c.Denominator, 10)
	return nOK && dOK && n.Sign() > 0 && d.Sign() > 0 && n.IsInt64() && d.IsInt64() && n.String() == c.Numerator && d.String() == c.Denominator
}

func nativePricingSource(source string) bool {
	return source == "llmrouter_native" || source == NativeCreditPricingSource
}

func (s *Session) NativeCreditConversion() (*NativeConversion, error) {
	if s == nil || !s.NativePricing() {
		return nil, ErrInvalid
	}
	if s.call.PricingSource != NativeCreditPricingSource {
		return nil, nil
	}
	if s.nativeCreditConversion == nil || !s.nativeCreditConversion.validCredit() {
		return nil, ErrInvalid
	}
	conversion := *s.nativeCreditConversion
	return &conversion, nil
}

// A CREDIT top-up may append source batches. Existing allocations and the
// original payer remain immutable; a changed source is never silently adopted.
func (remote RemoteCall) matchesNativeTopUp(row Call) bool {
	if row.Asset != "CREDIT/6" {
		return remote.matchesNative(row)
	}
	original := row.FundingSnapshot
	row.FundingSnapshot = ""
	if !remote.matchesNative(row) || remote.CashHoldAddress != "" {
		return false
	}
	var prior struct {
		CashHoldAddress string
		Allocations     []Allocation
	}
	if common.UnmarshalUnique([]byte(original), &prior) != nil || prior.CashHoldAddress != "" || len(remote.Allocations) < len(prior.Allocations) {
		return false
	}
	for i, a := range prior.Allocations {
		if !reflect.DeepEqual(a, remote.Allocations[i]) {
			return false
		}
	}
	sum := new(big.Int)
	for _, a := range remote.Allocations {
		n, ok := new(big.Int).SetString(a.Amount, 10)
		if !ok || n.Sign() <= 0 || !n.IsInt64() || a.BatchID == "" {
			return false
		}
		sum.Add(sum, n)
	}
	return sum.IsInt64() && sum.String() == remote.Held
}
