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
	Version   string `json:"version"`
	Operation string `json:"operation"`
	Policy    string `json:"policy"`
	Terms     []Term `json:"terms"`
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

var units = map[string]bool{"token": true, "character": true, "audio_second": true, "image": true, "request": true, "video_second": true, "provider_unit": true}
var ops = map[string]bool{"chat": true, "responses": true, "embedding": true, "speech": true, "transcription": true, "image_generation": true, "image_edit": true, "ocr": true, "video": true}

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
		compatible := t.Unit == "request" || t.Unit == "provider_unit"
		switch p.Operation {
		case "chat", "responses", "embedding":
			compatible = compatible || t.Unit == "token"
		case "speech":
			compatible = compatible || t.Unit == "character" || t.Unit == "audio_second" || t.Unit == "token"
		case "transcription":
			compatible = compatible || t.Unit == "audio_second" || t.Unit == "token"
		case "image_generation", "image_edit":
			compatible = compatible || t.Unit == "image"
		case "video":
			compatible = compatible || t.Unit == "video_second"
		}
		if !compatible {
			return errors.New("operation/unit mismatch")
		}
		if (t.Unit == "token" && t.Key != "input" && t.Key != "output" && t.Key != "cache_read" && t.Key != "cache_write" && t.Key != "reasoning") || (t.Unit == "character" && t.Key != "characters") || (t.Unit == "audio_second" && t.Key != "audio") || (t.Unit == "request" && t.Key != "requests") || (t.Unit == "video_second" && t.Key != "video") || (t.Unit == "provider_unit" && t.Key != "provider") {
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
		if t.Unit == "provider_unit" && strings.TrimSpace(t.ProviderUnit) == "" {
			return errors.New("provider namespace required")
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
	return nil
}
func ValidateUsage(p Price, u Usage) error {
	if err := ValidatePrice(p); err != nil {
		return err
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
	if len(seen) != len(terms) {
		return errors.New("missing measurement")
	}
	return nil
}
func Calculate(p Price, u Usage) (*big.Rat, error) {
	if e := ValidateUsage(p, u); e != nil {
		return nil, e
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
		n := new(big.Int)
		n.SetString(t.Price.Numerator, 10)
		d := new(big.Int)
		d.SetString(t.Price.Denominator, 10)
		sum.Add(sum, new(big.Rat).Mul(qv, new(big.Rat).SetFrac(n, d)))
	}
	return sum, nil
}
