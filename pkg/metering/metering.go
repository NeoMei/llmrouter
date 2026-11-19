package metering

import (
	"errors"
	"math/big"
	"strings"
)

type Quantity struct {
	Value string `json:"value"`
	Scale int    `json:"scale"`
}
type Rational struct {
	Numerator   string `json:"numerator"`
	Denominator string `json:"denominator"`
}
type Term struct {
	Key          string    `json:"key"`
	Unit         string    `json:"unit"`
	ProviderUnit string    `json:"provider_unit,omitempty"`
	Price        Rational  `json:"price"`
	Step         *Quantity `json:"step,omitempty"`
	Minimum      *Quantity `json:"minimum,omitempty"`
}
type Price struct {
	Version         string           `json:"version"`
	Operation       string           `json:"operation"`
	Policy          string           `json:"policy"`
	Terms           []Term           `json:"terms"`
	InputTokenBands []InputTokenBand `json:"input_token_bands,omitempty"`
}

// InputTokenBand prices every dimension of one request using its total input.
type InputTokenBand struct {
	MaxInputTokens int64               `json:"max_input_tokens"`
	Prices         map[string]Rational `json:"prices"`
}
type Measurement struct {
	Key          string   `json:"key"`
	Unit         string   `json:"unit"`
	ProviderUnit string   `json:"provider_unit,omitempty"`
	Quantity     Quantity `json:"quantity"`
}
type Usage struct {
	Version           string        `json:"version"`
	Operation         string        `json:"operation"`
	State             string        `json:"state"`
	Source            string        `json:"source"`
	Measurements      []Measurement `json:"measurements"`
	ProviderRequestID string        `json:"provider_request_id,omitempty"`
}

// WanCashTierPolicy uses CNY/6 micro-yuan per second; legacy native
// quota policy remains separate and retains its historical denomination.
func WanCashTierPolicy(policy string) bool {
	return policy == "ali_wan26_t2v_cny6_v2" || policy == "ali_wan27_t2v_cny6_v1"
}

var units = map[string]bool{"token": true, "character": true, "audio_second": true, "image": true, "request": true, "video_second": true, "provider_unit": true}
var ops = map[string]bool{"chat": true, "responses": true, "embedding": true, "rerank": true, "multimodal_embedding": true, "speech": true, "transcription": true, "image_generation": true, "image_edit": true, "ocr": true, "video": true}

func parseQuantity(v Quantity) (*big.Rat, error) {
	if v.Scale < 0 || v.Scale > 9 || v.Value == "" || (len(v.Value) > 1 && v.Value[0] == '0') || strings.Trim(v.Value, "0123456789") != "" || len(v.Value) > 38 {
		return nil, errors.New("invalid quantity")
	}
	n := new(big.Int)
	n.SetString(v.Value, 10)
	return new(big.Rat).SetFrac(n, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(v.Scale)), nil)), nil
}
func ValidatePrice(p Price) error {
	if p.Version != "metered_v2" || strings.TrimSpace(p.Policy) == "" || len(p.Policy) > 128 || !ops[p.Operation] || len(p.Terms) == 0 || len(p.Terms) > 64 {
		return errors.New("invalid price")
	}
	seen := map[string]bool{}
	for _, t := range p.Terms {
		if seen[t.Key] || strings.TrimSpace(t.Key) == "" || len(t.Key) > 128 || !units[t.Unit] || t.Price.Denominator == "0" || t.Price.Numerator == "" {
			return errors.New("invalid term")
		}
		seen[t.Key] = true
		if t.Unit != p.Terms[0].Unit {
			return errors.New("mixed tariff units")
		}
		compatible := false
		switch p.Operation {
		case "chat", "responses", "embedding", "rerank", "multimodal_embedding":
			compatible = t.Unit == "token"
		case "speech":
			compatible = t.Unit == "character" || (t.Unit == "provider_unit" && p.Policy == "elevenlabs_character_cost_v1" && t.ProviderUnit == "elevenlabs.character_cost.multilingual_v2.v1")
		case "transcription":
			compatible = t.Unit == "audio_second"
		case "image_generation", "image_edit":
			compatible = t.Unit == "image"
		case "ocr":
			compatible = (p.Policy == "success_request" && t.Unit == "request") || (p.Policy == "provider_tokens_v1" && t.Unit == "token")
		case "video":
			compatible = t.Unit == "video_second" || t.Unit == "provider_unit"
		}
		if !compatible {
			return errors.New("operation/unit mismatch")
		}
		tokenDimension := t.Key == "input" || t.Key == "output" || t.Key == "cache_read" || t.Key == "cache_write" || t.Key == "reasoning"
		switch p.Operation {
		case "rerank":
			tokenDimension = t.Key == "input"
		case "multimodal_embedding":
			tokenDimension = t.Key == "text" || t.Key == "visual"
		}
		videoKey := t.Key == "video"
		if p.Operation == "video" && (p.Policy == "ali_wan26_t2v_billable_duration_v1" || WanCashTierPolicy(p.Policy)) {
			videoKey = t.Key == "video_720p" || t.Key == "video_1080p"
		}
		if (t.Unit == "token" && !tokenDimension) || (t.Unit == "character" && t.Key != "characters") || (t.Unit == "audio_second" && t.Key != "audio") || (t.Unit == "request" && t.Key != "requests") || (t.Unit == "video_second" && !videoKey) || (t.Unit == "provider_unit" && t.Key != "provider") {
			return errors.New("unknown token dimension")
		}

		n, err := parseQuantity(Quantity{Value: t.Price.Numerator})
		if err != nil || n.Sign() < 0 {
			return errors.New("invalid price numerator")
		}
		d, err := parseQuantity(Quantity{Value: t.Price.Denominator})
		if err != nil || d.Sign() <= 0 {
			return errors.New("invalid denominator")
		}
		if t.Unit == "provider_unit" && (strings.TrimSpace(t.ProviderUnit) == "" || len(t.ProviderUnit) > 128) {
			return errors.New("provider namespace required")
		}
		if t.Unit != "provider_unit" && t.ProviderUnit != "" {
			return errors.New("unexpected provider namespace")
		}
		if t.Step != nil {
			if (t.Unit == "token" || t.Unit == "character" || t.Unit == "image" || t.Unit == "request") && t.Step.Scale != 0 {
				return errors.New("integer scale")
			}
			if _, e := parseQuantity(*t.Step); e != nil {
				return e
			}
			if t.Step.Value == "0" {
				return errors.New("invalid step")
			}
		}
		if t.Minimum != nil {
			if (t.Unit == "token" || t.Unit == "character" || t.Unit == "image" || t.Unit == "request") && t.Minimum.Scale != 0 {
				return errors.New("integer scale")
			}
			if _, e := parseQuantity(*t.Minimum); e != nil {
				return e
			}
		}
	}
	if p.Operation == "multimodal_embedding" && (!seen["text"] || !seen["visual"]) {
		return errors.New("missing multimodal token dimension")
	}
	if p.Operation == "ocr" {
		if p.Policy == "success_request" && (!seen["requests"] || len(p.Terms) != 1) {
			return errors.New("invalid request tariff")
		}
		if p.Policy == "provider_tokens_v1" && (!seen["input"] || !seen["output"] || len(p.Terms) != 2) {
			return errors.New("invalid provider token tariff")
		}
	}
	if p.Operation == "video" && (p.Policy == "ali_wan26_t2v_billable_duration_v1" || WanCashTierPolicy(p.Policy)) {
		if len(p.Terms) != 2 || !seen["video_720p"] || !seen["video_1080p"] {
			return errors.New("missing Wan resolution tier")
		}
	}
	if len(p.InputTokenBands) != 0 {
		if p.Operation != "chat" || p.Policy != "provider_reported_v1" || !seen["input"] || !seen["output"] || len(p.InputTokenBands) > 16 {
			return errors.New("invalid input token bands")
		}
		var previous int64
		for _, band := range p.InputTokenBands {
			if band.MaxInputTokens <= previous || band.MaxInputTokens > 1<<20 || len(band.Prices) != len(p.Terms) {
				return errors.New("invalid input token band")
			}
			previous = band.MaxInputTokens
			for _, term := range p.Terms {
				if _, ok := rationalPrice(band.Prices[term.Key]); !ok || term.Minimum != nil || term.Step != nil {
					return errors.New("invalid input token band term")
				}
			}
		}
	}
	return nil
}
func ValidateUsage(p Price, u Usage) error {
	if err := ValidatePrice(p); err != nil {
		return err
	}
	if len(u.Source) > 128 || len(u.ProviderRequestID) > 256 {
		return errors.New("usage identifier too long")
	}
	if u.Version != "metered_v2" || u.Operation != p.Operation || u.State != "confirmed" {
		return errors.New("usage not confirmed")
	}
	terms := map[string]Term{}
	for _, t := range p.Terms {
		terms[t.Key] = t
	}
	seen := map[string]bool{}
	for _, m := range u.Measurements {
		t, ok := terms[m.Key]
		if !ok || seen[m.Key] || m.Unit != t.Unit || m.ProviderUnit != t.ProviderUnit {
			return errors.New("measurement mismatch")
		}
		if _, e := parseQuantity(m.Quantity); e != nil {
			return e
		}
		if (m.Unit == "token" || m.Unit == "character" || m.Unit == "image" || m.Unit == "request") && m.Quantity.Scale != 0 {
			return errors.New("integer scale")
		}
		seen[m.Key] = true
	}
	selectedTier := p.Operation == "video" && (p.Policy == "ali_wan26_t2v_billable_duration_v1" || WanCashTierPolicy(p.Policy))
	if selectedTier && (len(seen) != 1 || (!seen["video_720p"] && !seen["video_1080p"])) {
		return errors.New("missing selected video resolution")
	}
	if !selectedTier && len(seen) != len(terms) {
		return errors.New("missing measurement")
	}
	return nil
}
func Calculate(p Price, u Usage) (*big.Rat, error) {
	if e := ValidateUsage(p, u); e != nil {
		return nil, e
	}
	var bandPrices map[string]Rational
	if len(p.InputTokenBands) != 0 {
		input, _ := tokenTotals(u)
		for _, band := range p.InputTokenBands {
			if input.Cmp(big.NewInt(band.MaxInputTokens)) <= 0 {
				bandPrices = band.Prices
				break
			}
		}
		if bandPrices == nil {
			return nil, errors.New("input exceeds frozen tariff bands")
		}
	}
	sum := new(big.Rat)
	for _, m := range u.Measurements {
		var t Term
		for _, x := range p.Terms {
			if x.Key == m.Key {
				t = x
				break
			}
		}
		qv, _ := parseQuantity(m.Quantity)
		if qv.Sign() == 0 {
			continue
		}
		if t.Minimum != nil {
			minimum, _ := parseQuantity(*t.Minimum)
			if qv.Cmp(minimum) < 0 {
				qv.Set(minimum)
			}
		}
		if t.Step != nil {
			step, _ := parseQuantity(*t.Step)
			ratio := new(big.Rat).Quo(qv, step)
			whole, remainder := new(big.Int), new(big.Int)
			whole.QuoRem(ratio.Num(), ratio.Denom(), remainder)
			if remainder.Sign() > 0 {
				whole.Add(whole, big.NewInt(1))
			}
			qv.Mul(new(big.Rat).SetInt(whole), step)
		}
		price := t.Price
		if bandPrices != nil {
			price = bandPrices[t.Key]
		}
		rate, _ := rationalPrice(price)
		sum.Add(sum, new(big.Rat).Mul(qv, rate))
	}
	return sum, nil
}

func rationalPrice(price Rational) (*big.Rat, bool) {
	n, en := parseQuantity(Quantity{Value: price.Numerator})
	d, ed := parseQuantity(Quantity{Value: price.Denominator})
	if en != nil || ed != nil || d.Sign() == 0 {
		return nil, false
	}
	return n.Quo(n, d), true
}

func inputTokenTerm(key string) bool {
	return key == "input" || key == "cache_read" || key == "cache_write"
}

func tokenTotals(u Usage) (input, output *big.Int) {
	input, output = new(big.Int), new(big.Int)
	for _, m := range u.Measurements {
		quantity, _ := new(big.Int).SetString(m.Quantity.Value, 10)
		if inputTokenTerm(m.Key) {
			input.Add(input, quantity)
		} else {
			output.Add(output, quantity)
		}
	}
	return input, output
}

// CalculateReservation selects the most expensive whole request that the
// certified bounds allow. Exclusive dimensions share an input or output cap.
func CalculateReservation(p Price, u Usage) (*big.Rat, error) {
	if len(p.InputTokenBands) == 0 {
		return Calculate(p, u)
	}
	if err := ValidateUsage(p, u); err != nil {
		return nil, err
	}
	input, output, err := reservationTokenBounds(u)
	if err != nil {
		return nil, err
	}
	maximum := new(big.Rat)
	for _, band := range p.InputTokenBands {
		inputRate, outputRate := new(big.Rat), new(big.Rat)
		for key, price := range band.Prices {
			rate, _ := rationalPrice(price)
			greatest := outputRate
			if inputTokenTerm(key) {
				greatest = inputRate
			}
			if rate.Cmp(greatest) > 0 {
				greatest.Set(rate)
			}
		}
		bandInput := big.NewInt(band.MaxInputTokens)
		reached := input.Cmp(bandInput) <= 0
		if reached {
			bandInput = input
		}
		cost := inputRate.Mul(inputRate, new(big.Rat).SetInt(bandInput))
		cost.Add(cost, outputRate.Mul(outputRate, new(big.Rat).SetInt(output)))
		if cost.Cmp(maximum) > 0 {
			maximum.Set(cost)
		}
		if reached {
			return maximum, nil
		}
	}
	return nil, errors.New("input exceeds frozen tariff bands")
}

func reservationTokenBounds(u Usage) (input, output *big.Int, err error) {
	for _, m := range u.Measurements {
		quantity, _ := new(big.Int).SetString(m.Quantity.Value, 10)
		bound := &output
		if inputTokenTerm(m.Key) {
			bound = &input
		}
		if *bound == nil {
			*bound = quantity
		} else if quantity.Cmp(*bound) != 0 {
			return nil, nil, errors.New("exclusive token bounds differ")
		}
	}
	return input, output, nil
}

// ValidateBandedSettlement enforces the admitted input and output caps against
// complete supplier evidence, including cached input and reasoning output.
func ValidateBandedSettlement(p Price, reservation, usage Usage) error {
	if err := ValidateUsage(p, usage); err != nil {
		return err
	}
	reservation.State = "confirmed"
	if err := ValidateUsage(p, reservation); err != nil {
		return err
	}
	maxInput, maxOutput, err := reservationTokenBounds(reservation)
	if err != nil {
		return err
	}
	input, output := tokenTotals(usage)
	if input.Cmp(maxInput) > 0 || output.Cmp(maxOutput) > 0 {
		return errors.New("supplier usage exceeds frozen reservation")
	}
	return nil
}
