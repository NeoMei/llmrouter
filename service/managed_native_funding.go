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

func beginManagedNativeFunding(ctx context.Context, info *relaycommon.RelayInfo, quota int, native *model.NativeAdmission) error {
	if !info.ManagedNativePricing() || info.QuotaClamp != nil || quota < 0 || quota > common.MaxQuota || info.Billing != nil {
		return errors.New("managed native funding admission unavailable")
	}
	if err := FreezeManagedSupplementalPrices(info); err != nil {
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
	info.BillingSource, info.FinalPreConsumedQuota = "unihub", quota
	info.Billing = &managedNativeFunding{info: info, reserved: quota}
	return nil
}

// ReserveManagedNativePrice binds native calculations to the original managed
// credential and product wallet. Native OCR and synchronous relay share it.
func ReserveManagedNativePrice(ctx context.Context, session *platformbilling.Session, billingModel, group string, routeID, channelID, quota int, priceSnapshot []byte) error {
	numerator, denominator, err := model.ProductCashConversionRate()
	if err != nil {
		return err
	}
	conversion := platformbilling.NativeConversion{Version: model.ProductCashConversionVersion, Numerator: numerator, Denominator: denominator, Rounding: model.ProductCashRoundingVersion}
	snapshot, err := common.Marshal(struct {
		PriceSnapshot string                           `json:"price_snapshot"`
		Conversion    platformbilling.NativeConversion `json:"conversion"`
	}{string(priceSnapshot), conversion})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(snapshot)
	quote := platformbilling.NativeQuote{PricingVersion: "lr-native-v1", PricingDigest: hex.EncodeToString(digest[:]), BillingModel: billingModel, WireOperation: session.WireOperation().ID, RouteID: strconv.Itoa(routeID), ChannelID: strconv.Itoa(channelID), Group: group, NativeQuotaMax: strconv.Itoa(quota), Conversion: conversion, Asset: "CNY/6"}
	quote.MaxAmount, err = quote.Amount(quota)
	if err != nil {
		return err
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
	return s.info.PlatformBilling.SettleNative(ctx, actual, usage)
}
