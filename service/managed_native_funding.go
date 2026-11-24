package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

type managedNativeFunding struct {
	info     *relaycommon.RelayInfo
	reserved int
	settled  bool
}

// ManagedNativeBillingError preserves reserve certainty and the bounded public
// funding refusal across native metered and quota-based relay entry points.
func ManagedNativeBillingError(err error) *types.NewAPIError {
	code := types.ErrorCodeUpdateDataError
	if errors.Is(err, platformbilling.ErrNativeCreditsRequired) {
		code = types.ErrorCodePlatformModelCreditsRequired
	}
	if errors.Is(err, platformbilling.ErrProductMembershipExpired) {
		code = types.ErrorCodeProductMembershipExpired
	}
	if errors.Is(err, platformbilling.ErrManagedModelNotAllowed) {
		code = types.ErrorCode(platformbilling.ErrManagedModelNotAllowed.Error())
	}
	return types.NewErrorWithStatusCode(err, code, platformbilling.HTTPStatus(err), types.ErrOptionWithSkipRetry())
}

func CalculateManagedNativeQuota(snapshotJSON string, usage metering.Usage) (int, error) {
	var wrapper struct {
		PriceSnapshot string `json:"price_snapshot"`
	}
	var snapshot struct {
		Native *model.NativeAdmission `json:"native"`
	}
	if common.UnmarshalUnique([]byte(snapshotJSON), &wrapper) != nil || common.UnmarshalUnique([]byte(wrapper.PriceSnapshot), &snapshot) != nil || snapshot.Native == nil {
		return 0, platformbilling.ErrInvalid
	}
	quota, clamp, err := model.NativeQuota(*snapshot.Native, usage, false)
	if err != nil {
		return 0, err
	}
	if clamp != nil {
		return 0, clamp
	}
	return quota, nil
}

func CalculateProductCreditAmount(snapshotJSON string, usage metering.Usage) (int64, error) {
	var wrapper struct {
		PriceSnapshot string `json:"price_snapshot"`
	}
	var snapshot struct {
		Native *model.NativeAdmission `json:"native"`
	}
	if common.UnmarshalUnique([]byte(snapshotJSON), &wrapper) != nil ||
		common.UnmarshalUnique([]byte(wrapper.PriceSnapshot), &snapshot) != nil || snapshot.Native == nil {
		return 0, platformbilling.ErrInvalid
	}
	return model.NativeProductCreditAmount(*snapshot.Native, usage, false)
}

func ManagedNativeAmount(native model.NativeAdmission, usage metering.Usage, reserve bool) (int64, error) {
	if native.ProductPrice != nil {
		return model.NativeProductCreditAmount(native, usage, reserve)
	}
	quota, clamp, err := model.NativeQuota(native, usage, reserve)
	if err != nil {
		return 0, err
	}
	if clamp != nil {
		return 0, clamp
	}
	return int64(quota), nil
}

func beginManagedNativeFunding(ctx context.Context, info *relaycommon.RelayInfo, quota int64, native *model.NativeAdmission) error {
	if !info.ManagedNativePricing() || info.QuotaClamp != nil || quota < 0 || quota > int64(common.MaxQuota) && !info.PlatformBilling.ProductCredit() || info.Billing != nil {
		return errors.New("managed native funding admission unavailable")
	}
	if info.PlatformBilling.ProductCredit() {
		if native == nil || native.ProductPrice == nil || native.GroupRatio != "1" || info.ManagedNativeSupplemental != nil {
			return platformbilling.ErrInvalid
		}
	} else if err := FreezeManagedSupplementalPrices(info); err != nil {
		return err
	}
	snapshot, err := common.Marshal(struct {
		Price        any                              `json:"price"`
		OtherRatios  map[string]float64               `json:"other_ratios,omitempty"`
		Tiered       any                              `json:"tiered"`
		Native       *model.NativeAdmission           `json:"native,omitempty"`
		Model        string                           `json:"model"`
		Group        string                           `json:"group"`
		Supplemental *types.SupplementalPriceSnapshot `json:"supplemental"`
	}{info.PriceData, info.PriceData.OtherRatios(), info.TieredBillingSnapshot, native, info.BillingModelNameOrOrigin(), info.UsingGroup, info.ManagedNativeSupplemental})
	if err != nil {
		return err
	}
	if err := ReserveManagedNativePrice(ctx, info.PlatformBilling, info.BillingModelNameOrOrigin(), info.UsingGroup, info.ModelRouteId, info.ChannelId, quota, snapshot); err != nil {
		return err
	}
	info.BillingSource = "unihub"
	// Product amounts are ledger units and never local quota. The journal
	// owns their signed 64-bit reservation and settlement.
	if !info.PlatformBilling.ProductCredit() {
		info.FinalPreConsumedQuota = int(quota)
	}
	info.Billing = &managedNativeFunding{info: info, reserved: info.FinalPreConsumedQuota}
	return nil
}

// ReserveManagedNativePrice binds native calculations to the original managed
// credential and product wallet. Native OCR and synchronous relay share it.
func ReserveManagedNativePrice(ctx context.Context, session *platformbilling.Session, billingModel, group string, routeID, channelID int, quota int64, priceSnapshot []byte) error {
	credit, err := session.NativeCreditConversion()
	if err != nil {
		return err
	}
	pricingVersion, asset := "lr-native-v1", "CNY/6"
	var conversion platformbilling.NativeConversion
	if credit != nil {
		conversion = *credit
		pricingVersion, asset = platformbilling.NativeCreditPricingVersion, "CREDIT/6"
	} else {
		numerator, denominator, e := model.ProductCashConversionRate()
		if e != nil {
			return e
		}
		conversion = platformbilling.NativeConversion{Version: model.ProductCashConversionVersion, Numerator: numerator, Denominator: denominator, Rounding: model.ProductCashRoundingVersion}
	}
	snapshot, err := common.Marshal(struct {
		PriceSnapshot string                           `json:"price_snapshot"`
		Conversion    platformbilling.NativeConversion `json:"conversion"`
	}{string(priceSnapshot), conversion})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(snapshot)
	quote := platformbilling.NativeQuote{PricingVersion: pricingVersion, PricingDigest: hex.EncodeToString(digest[:]), BillingModel: billingModel, WireOperation: session.WireOperation().ID, RouteID: strconv.Itoa(routeID), ChannelID: strconv.Itoa(channelID), Group: group, NativeQuotaMax: strconv.FormatInt(quota, 10), Conversion: conversion, Asset: asset}
	scope := ""
	if session.ProductCredit() {
		scope = platformbilling.ProductCreditScope
	}
	quote.MaxAmount, err = quote.AmountForScope(quota, scope)
	if err != nil {
		return err
	}
	if session.ProductCredit() {
		var frozen struct {
			Native *model.NativeAdmission `json:"native"`
		}
		if common.UnmarshalUnique(priceSnapshot, &frozen) != nil || frozen.Native == nil {
			return platformbilling.ErrInvalid
		}
		native := frozen.Native
		if native.Price.Policy == "success_request" && native.Price.Operation == "ocr" ||
			native.Price.Policy == "unicode_scalar_v1" && native.Price.Operation == "speech" ||
			session.WireOperation().ID == "videos.create" && native.Price.Operation == "video" {
			// Only exact request-derived dimensions prove a minimum. Token
			// estimates and maximum output bounds never trigger auto-rebuy.
			minimum, e := model.NativeProductCreditAmount(*native, native.Reservation, false)
			if e == nil && minimum > 0 {
				return session.ReserveNative(ctx, quote, string(snapshot), minimum)
			}
		}
	}
	return session.ReserveNative(ctx, quote, string(snapshot))
}

func (s *managedNativeFunding) Reserve(target int) error {
	if target <= s.reserved {
		return nil
	}
	return errors.New("managed native dispatch price is immutable")
}
func (s *managedNativeFunding) GetPreConsumedQuota() int { return s.reserved }
func (s *managedNativeFunding) NeedsRefund() bool        { return false }
func (s *managedNativeFunding) Refund(*gin.Context)      {}
func (s *managedNativeFunding) MarkReconciliationPending(reason string) {
	// Managed native funding is fenced by durable dispatch state. A failed
	// settlement stays pending for the native recovery path.
	common.SysError("managed native funding requires reconciliation: " + reason)
}
func (s *managedNativeFunding) IsSettled() bool { return s.settled }
func (s *managedNativeFunding) Settle(actual int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var usage *platformbilling.Usage
	switch s.info.PlatformBilling.WireOperation().ID {
	case "chat.completions.create", "claude.messages.create", "gemini.generate", "responses.create", "responses.compact", "embeddings.create":
		if s.info.ManagedNativeEvidence == nil || s.info.ManagedNativeEvidence.State != "confirmed" {
			return platformbilling.ErrPending
		}
		usage = &platformbilling.Usage{Metered: s.info.ManagedNativeEvidence}
	}
	if err := s.info.PlatformBilling.SettleNative(ctx, actual, usage); err != nil {
		return err
	}
	s.settled = true
	return nil
}
