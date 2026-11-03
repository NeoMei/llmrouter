package model

import (
	"context"
	"fmt"
	"gorm.io/gorm"
	"math"
	"math/big"
	"strconv"

	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/shopspring/decimal"
)

const ProductCashConversionVersion = "lr-quota-usd-cny-v1"
const ProductCashRoundingVersion = "lr-cumulative-ceil-v1"

// currentProductCashRate freezes decimal configuration as an exact rational:
// LR quota / QuotaPerUnit USD * USDExchangeRate CNY * 1e6 micro-CNY.
func currentProductCashRate() (string, string, error) {
	rate, unit := operation_setting.USDExchangeRate, common.QuotaPerUnit
	if rate <= 0 || unit <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) || math.IsNaN(unit) || math.IsInf(unit, 0) {
		return "", "", ErrProductFundingConflict
	}
	r, ok := new(big.Rat).SetString(strconv.FormatFloat(rate, 'g', -1, 64))
	if !ok {
		return "", "", ErrProductFundingConflict
	}
	u, ok := new(big.Rat).SetString(strconv.FormatFloat(unit, 'g', -1, 64))
	if !ok {
		return "", "", ErrProductFundingConflict
	}
	r.Quo(r, u).Mul(r, big.NewRat(1000000, 1))
	if !r.Num().IsInt64() || !r.Denom().IsInt64() {
		return "", "", ErrProductFundingConflict
	}
	return r.Num().String(), r.Denom().String(), nil
}

func productCallCashRate(call ProductRuntimeCall) (*big.Rat, error) {
	n, nOK := new(big.Int).SetString(call.ConversionNumerator, 10)
	d, dOK := new(big.Int).SetString(call.ConversionDenominator, 10)
	if call.Asset != userkit.RuntimeCashAsset || call.UnitVersion != userkit.RuntimeCashUnitVersion || call.ConversionVersion != ProductCashConversionVersion || !nOK || !dOK || n.Sign() <= 0 || d.Sign() <= 0 || !n.IsInt64() || !d.IsInt64() || n.String() != call.ConversionNumerator || d.String() != call.ConversionDenominator {
		return nil, ErrProductFundingConflict
	}
	return new(big.Rat).SetFrac(n, d), nil
}

func productCashPricingDigest(snapshot string, call ProductRuntimeCall) string {
	return productDigest(snapshot + ":" + call.Asset + ":" + call.UnitVersion + ":" + call.ConversionVersion + ":" + call.CashRoundingVersion + ":" + call.ConversionNumerator + ":" + call.ConversionDenominator)
}

// productCallContractDigest binds a supplemental cash segment's quota offset
// without changing the parent pricing digest required by the central ledger.
func productCallContractDigest(call ProductRuntimeCall, model, operation string) string {
	raw := fmt.Sprintf("%s:%d:%d:%s:%s:%s:%s:%t:%t:%s", call.ScopeDigest, call.UserID, call.TokenID, call.AccountID, model, operation, call.PricingDigest, call.ExternalCounters, call.FinancialCommitment, call.ParentCallID)
	if call.Asset == userkit.RuntimeCashAsset {
		raw += fmt.Sprintf(":%s:%d", call.CashRoundingVersion, call.CashBaseQuota)
	}
	return productDigest(raw)
}

// ProductRuntimeCallContract validates the frozen wire contract, including rows
// admitted before explicit asset fields were added. It never reads live rates.
func ProductRuntimeCallContract(call ProductRuntimeCall) (userkit.RuntimeReserveRequest, error) {
	var req userkit.RuntimeReserveRequest
	if err := common.UnmarshalJsonStr(call.ReserveJSON, &req); err != nil {
		return req, ErrProductFundingConflict
	}
	if req.CallID != call.ID || req.ParentCallID != call.ParentCallID || req.LocalUserID != strconv.Itoa(call.UserID) || req.LocalKeyID != strconv.Itoa(call.TokenID) || req.AccountID != call.AccountID || req.RequestDigest != call.ContractDigest || req.PricingDigest != call.PricingDigest {
		return req, ErrProductFundingConflict
	}
	if _, e := productRuntimeAmount(req.Amount); e != nil {
		return req, e
	}
	if req.Asset == userkit.RuntimeAsset && req.UnitVersion == userkit.RuntimeUnitVersion {
		if (call.Asset != "" && call.Asset != req.Asset) || (call.UnitVersion != "" && call.UnitVersion != req.UnitVersion) || call.ConversionVersion != "" || call.ConversionNumerator != "" || call.ConversionDenominator != "" || call.CashBaseQuota != 0 || call.CashRoundingVersion != "" {
			return req, ErrProductFundingConflict
		}
		return req, nil
	}
	if req.Asset != call.Asset || req.UnitVersion != call.UnitVersion || req.PriceVersion != userkit.RuntimeCashUnitVersion {
		return req, ErrProductFundingConflict
	}
	if call.CashRoundingVersion != ProductCashRoundingVersion || call.CashBaseQuota < 0 || call.CashBaseQuota > common.MaxQuota || (call.ParentCallID == "" && call.CashBaseQuota != 0) || productCallContractDigest(call, req.Model, req.Operation) != call.ContractDigest {
		return req, ErrProductFundingConflict
	}
	if _, err := productCallCashRate(call); err != nil {
		return req, err
	}
	if call.SnapshotDigest == "" || productCashPricingDigest(call.SnapshotDigest, call) != call.PricingDigest {
		return req, ErrProductFundingConflict
	}
	return req, nil
}

// productCallAmount rounds the cumulative monetary charge up once. Native key
// limits and usage counters remain LR quota throughout the state machine.
func productCallAmount(call ProductRuntimeCall, quota int) (int64, error) {
	if quota < 0 || quota > common.MaxQuota {
		return 0, ErrProductFundingConflict
	}
	if call.Asset == "" || call.Asset == userkit.RuntimeAsset {
		return int64(quota), nil
	}
	rate, err := productCallCashRate(call)
	if err != nil {
		return 0, err
	}
	if call.CashRoundingVersion != ProductCashRoundingVersion || call.CashBaseQuota < 0 || int64(call.CashBaseQuota)+int64(quota) > int64(common.MaxQuota) {
		return 0, ErrProductFundingConflict
	}
	before := new(big.Int).Mul(rate.Num(), big.NewInt(int64(call.CashBaseQuota)))
	after := new(big.Int).Mul(rate.Num(), big.NewInt(int64(call.CashBaseQuota)+int64(quota)))
	// ceil(n/d) = (n+d-1)/d for non-negative integers. Subtract the frozen
	// cumulative base so child segments, topups and refunds telescope exactly.
	rounding := new(big.Int).Sub(rate.Denom(), big.NewInt(1))
	before.Quo(before.Add(before, rounding), rate.Denom())
	after.Quo(after.Add(after, rounding), rate.Denom())
	amount := new(big.Int).Sub(after, before)
	if !amount.IsInt64() {
		return 0, ErrProductFundingConflict
	}
	return amount.Int64(), nil
}

func productRuntimeAmount(raw string) (int64, error) {
	n, e := strconv.ParseInt(raw, 10, 64)
	if e != nil || n < 0 || strconv.FormatInt(n, 10) != raw {
		return 0, ErrProductFundingConflict
	}
	return n, nil
}

// CashBalance exposes exact micro-CNY from UniHub. It is independent of the
// compatibility quota projection and never consults a local spendable balance.
func (s *ProductFundingStore) CashBalance(ctx context.Context, id int) (userkit.RuntimeBalance, error) {
	a, e := s.Account(ctx, id)
	if e != nil {
		return userkit.RuntimeBalance{}, e
	}
	if a.State != "central" {
		return userkit.RuntimeBalance{}, ErrProductFundingPending
	}
	b, e := s.client.BalanceAsset(ctx, strconv.Itoa(id), a.AccountID, userkit.RuntimeCashAsset, userkit.RuntimeCashUnitVersion)
	if e != nil {
		return b, e
	}
	if b.AccountID != a.AccountID || b.Asset != userkit.RuntimeCashAsset || b.UnitVersion != userkit.RuntimeCashUnitVersion {
		return b, ErrProductFundingConflict
	}
	for _, v := range []string{b.Available, b.Held, b.Consumed} {
		if _, e = productRuntimeAmount(v); e != nil {
			return b, e
		}
	}
	return b, nil
}

// ProductMoneyBalance is the exact ledger snapshot accompanying the legacy
// quota compatibility field in the original product user DTO.
type ProductMoneyBalance struct {
	Mode        string `json:"mode"`
	Asset       string `json:"asset"`
	UnitVersion string `json:"unit_version"`
	Available   string `json:"available"`
	Held        string `json:"held"`
	Consumed    string `json:"consumed"`
}

func productCashQuotaProjection(available string) (int, error) {
	amount, e := productRuntimeAmount(available)
	if e != nil {
		return 0, e
	}
	numerator, denominator, e := currentProductCashRate()
	if e != nil {
		return 0, e
	}
	n, _ := new(big.Int).SetString(numerator, 10)
	d, _ := new(big.Int).SetString(denominator, 10)
	projected := new(big.Int).Quo(new(big.Int).Mul(big.NewInt(amount), d), n)
	return common.QuotaFromDecimal(decimal.NewFromBigInt(projected, 0)), nil
}

// productCashSegments returns remaining settled segments in reverse quota order.
// All additions start at the current net quota; only the newest remaining
// segment may be refunded, keeping every family prefix rounded exactly once.
func (s *ProductFundingStore) productCashSegments(ctx context.Context, tx *gorm.DB, parent ProductRuntimeCall) ([]ProductRuntimeCall, int, error) {
	if parent.ParentCallID != "" || parent.Asset != userkit.RuntimeCashAsset {
		return nil, 0, ErrProductFundingConflict
	}
	var calls []ProductRuntimeCall
	if e := tx.WithContext(ctx).Where("id = ? OR parent_call_id = ?", parent.ID, parent.ID).Order("cash_base_quota DESC,created_at DESC,id DESC").Find(&calls).Error; e != nil {
		return nil, 0, e
	}
	hasRefunds := tx.Migrator().HasTable(&ProductCallRefund{})
	total := int64(0)
	for i := range calls {
		call := &calls[i]
		if call.ScopeDigest != parent.ScopeDigest || call.UserID != parent.UserID || call.TokenID != parent.TokenID || call.AccountID != parent.AccountID || call.Asset != parent.Asset || call.UnitVersion != parent.UnitVersion || call.PricingDigest != parent.PricingDigest || call.ConversionNumerator != parent.ConversionNumerator || call.ConversionDenominator != parent.ConversionDenominator || call.CashRoundingVersion != parent.CashRoundingVersion {
			return nil, 0, ErrProductFundingConflict
		}
		if _, e := ProductRuntimeCallContract(*call); e != nil {
			return nil, 0, e
		}
		if call.State == "released" || call.State == "rejected" {
			call.FinalQuota = 0
			continue
		}
		if call.State != "settled" {
			return nil, 0, ErrProductFundingPending
		}
		refunded := int64(0)
		if hasRefunds {
			var receipts []ProductCallRefund
			if e := tx.Where("call_id = ? AND state IN ?", call.ID, []string{"pending", "applied"}).Find(&receipts).Error; e != nil {
				return nil, 0, e
			}
			for _, r := range receipts {
				if r.State == "pending" {
					return nil, 0, ErrProductFundingPending
				}
				if r.Amount <= 0 {
					return nil, 0, ErrProductFundingConflict
				}
				refunded += int64(r.Amount)
			}
		}
		if refunded > int64(call.FinalQuota) {
			return nil, 0, ErrProductFundingConflict
		}
		call.FinalQuota -= int(refunded)
		total += int64(call.FinalQuota)
		if total > int64(common.MaxQuota) {
			return nil, 0, ErrProductFundingConflict
		}
	}
	return calls, int(total), nil
}
