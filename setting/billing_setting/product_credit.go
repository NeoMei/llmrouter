package billing_setting

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/metering"
)

type productCreditSupplierTariff struct {
	Version  string         `json:"version"`
	Currency string         `json:"currency"`
	Tariff   metering.Price `json:"tariff"`
}

type productCreditPricing struct {
	Version    string                                                       `json:"version"`
	Multiplier *metering.Rational                                           `json:"multiplier,omitempty"`
	Suppliers  map[string]map[string]map[string]productCreditSupplierTariff `json:"suppliers,omitempty"`
}

// ProductCreditPrice captures public pricing and the configured supplier
// tariff versions that informed it. A supplier's actual charge is independent.
type ProductCreditPrice struct {
	Model           string                                 `json:"model"`
	Tariff          metering.Price                         `json:"tariff"`
	Source          string                                 `json:"source"`
	Revision        string                                 `json:"revision"`
	PolicyVersion   string                                 `json:"policy_version,omitempty"`
	Multiplier      metering.Rational                      `json:"multiplier"`
	CNYPerUSD       string                                 `json:"cny_per_usd,omitempty"`
	SupplierTariffs map[string]productCreditSupplierTariff `json:"supplier_tariffs,omitempty"`
	EvidenceDigest  string                                 `json:"evidence_digest,omitempty"`
}

var (
	productCreditPricingMu      sync.RWMutex
	productCreditPricingConfig  = productCreditPricing{Version: "product-credit-pricing-v1"}
	productCreditPricingVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	errProductCreditPricing     = errors.New("invalid product credit supplier tariff or multiplier")
)

func parseProductCreditPricing(raw string) (productCreditPricing, error) {
	var config productCreditPricing
	if len(raw) > 4<<20 || common.UnmarshalUnique([]byte(raw), &config) != nil || !productCreditPricingVersion.MatchString(config.Version) || len(config.Suppliers) > 10000 {
		return config, errProductCreditPricing
	}
	d := common.NewJsonDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&config) != nil {
		return config, errProductCreditPricing
	}
	if config.Multiplier != nil {
		for _, part := range []string{config.Multiplier.Numerator, config.Multiplier.Denominator} {
			if len(part) == 0 || len(part) > 9 || part[0] == '0' || strings.Trim(part, "0123456789") != "" {
				return config, errProductCreditPricing
			}
		}
	}
	for model, operations := range config.Suppliers {
		if model == "" || strings.TrimSpace(model) != model || len(model) > 128 || len(operations) > 16 {
			return config, errProductCreditPricing
		}
		for operation, suppliers := range operations {
			if len(suppliers) < 1 || len(suppliers) > 32 {
				return config, errProductCreditPricing
			}
			var comparable *metering.Price
			for supplier, evidence := range suppliers {
				if !productCreditPricingVersion.MatchString(supplier) || !productCreditPricingVersion.MatchString(evidence.Version) || evidence.Currency != "USD" && evidence.Currency != "CNY" ||
					ValidateModelNativePricing(map[string]map[string]metering.Price{model: {operation: evidence.Tariff}}) != nil {
					return config, errProductCreditPricing
				}
				price := evidence.Tariff
				slices.SortFunc(price.Terms, func(a, b metering.Term) int { return strings.Compare(a.Key, b.Key) })
				if comparable == nil {
					comparable = &price
					continue
				}
				if comparable.Policy != price.Policy || len(comparable.Terms) != len(price.Terms) {
					return config, errProductCreditPricing
				}
				for i, term := range price.Terms {
					base := comparable.Terms[i]
					term.Price = base.Price
					if !reflect.DeepEqual(base, term) {
						return config, errProductCreditPricing
					}
				}
			}
		}
	}
	return config, nil
}

func ValidateProductCreditPricing(raw string) error {
	_, err := parseProductCreditPricing(raw)
	return err
}

func SetProductCreditPricing(raw string) error {
	pricing, err := parseProductCreditPricing(raw)
	if err != nil {
		return err
	}
	productCreditPricingMu.Lock()
	productCreditPricingConfig = pricing
	productCreditPricingMu.Unlock()
	return nil
}

func ProductCreditPricingJSONString() string {
	productCreditPricingMu.RLock()
	defer productCreditPricingMu.RUnlock()
	raw, _ := common.Marshal(productCreditPricingConfig)
	return string(raw)
}

// ResolveProductCreditPrice requires an exact canonical public model and
// operation. Supplier tariffs use major currency units per single metered
// dimension; CNY prices are converted to USD before computing each maximum.
func ResolveProductCreditPrice(model, operation, cnyPerUSD string) (ProductCreditPrice, bool) {
	fx, ok := new(big.Rat).SetString(cnyPerUSD)
	if !ok || fx.Sign() <= 0 {
		return ProductCreditPrice{}, false
	}
	quote := ProductCreditPrice{Model: model, CNYPerUSD: fx.RatString()}
	if price, ok := ResolveModelNativePrice(model, operation); ok {
		quote.Source, quote.Tariff = "published_override", price
	} else {
		productCreditPricingMu.RLock()
		p := productCreditPricingConfig
		productCreditPricingMu.RUnlock()
		suppliers := p.Suppliers[model][operation]
		if len(suppliers) == 0 {
			return ProductCreditPrice{}, false
		}
		quote.Source, quote.PolicyVersion = "supplier_paygo_max", p.Version
		quote.Multiplier = metering.Rational{Numerator: "3", Denominator: "2"}
		if p.Multiplier != nil {
			quote.Multiplier = *p.Multiplier
		}
		multiplier, _ := new(big.Rat).SetString(quote.Multiplier.Numerator + "/" + quote.Multiplier.Denominator)
		evidence, err := common.Marshal(suppliers)
		if err != nil || common.Unmarshal(evidence, &quote.SupplierTariffs) != nil {
			return ProductCreditPrice{}, false
		}
		digest := sha256.Sum256(evidence)
		quote.EvidenceDigest = hex.EncodeToString(digest[:])
		maxima := make(map[string]*big.Rat)
		for _, supplier := range suppliers {
			for _, term := range supplier.Tariff.Terms {
				rate, _ := new(big.Rat).SetString(term.Price.Numerator + "/" + term.Price.Denominator)
				if supplier.Currency == "CNY" {
					rate.Quo(rate, fx)
				}
				if prior := maxima[term.Key]; prior == nil || rate.Cmp(prior) > 0 {
					maxima[term.Key] = rate
				}
			}
			if quote.Tariff.Version == "" {
				quote.Tariff = supplier.Tariff
				quote.Tariff.Terms = slices.Clone(supplier.Tariff.Terms)
			}
		}
		for i, term := range quote.Tariff.Terms {
			rate := maxima[term.Key].Mul(maxima[term.Key], multiplier)
			quote.Tariff.Terms[i].Price = metering.Rational{Numerator: rate.Num().String(), Denominator: rate.Denom().String()}
		}
		if metering.ValidatePrice(quote.Tariff) != nil {
			return ProductCreditPrice{}, false
		}
	}
	raw, err := common.Marshal(quote)
	if err != nil {
		return ProductCreditPrice{}, false
	}
	digest := sha256.Sum256(raw)
	quote.Revision = hex.EncodeToString(digest[:])
	return quote, true
}
