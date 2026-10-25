package metering

import (
	"errors"
	"fmt"
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

func q(v Quantity) (*big.Rat, error) {
	if v.Scale < 0 || v.Scale > 9 || v.Value == "" || (len(v.Value) > 1 && v.Value[0] == '0') || strings.Trim(v.Value, "0123456789") != "" || len(v.Value) > 38 {
		return nil, errors.New("invalid quantity")
	}
	n := new(big.Int)
	n.SetString(v.Value, 10)
	return new(big.Rat).SetFrac(n, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(v.Scale)), nil)), nil
}
func ValidatePrice(p Price) error {
	if p.Version != "metered_v2" || p.Policy == "" || !ops[p.Operation] || len(p.Terms) == 0 || len(p.Terms) > 64 {
		return errors.New("invalid price")
	}
	seen := map[string]bool{}
	for _, t := range p.Terms {
		if seen[t.Key] || t.Key == "" || !units[t.Unit] || t.Price.Denominator == "0" || t.Price.Numerator == "" {
			return errors.New("invalid term")
		}
		seen[t.Key] = true
		n, ok := new(big.Int).SetString(t.Price.Numerator, 10)
		if !ok || n.Sign() < 0 || len(t.Price.Numerator) > 38 {
			return errors.New("invalid price numerator")
		}
		d, ok := new(big.Int).SetString(t.Price.Denominator, 10)
		if !ok || d.Sign() <= 0 {
			return errors.New("invalid denominator")
		}
		if t.Unit == "provider_unit" && t.ProviderUnit == "" {
			return errors.New("provider namespace required")
		}
		if t.Step != nil {
			if _, e := q(*t.Step); e != nil {
				return e
			}
			if t.Step.Value == "0" {
				return errors.New("invalid step")
			}
		}
		if t.Minimum != nil {
			if _, e := q(*t.Minimum); e != nil {
				return e
			}
		}
	}
	return nil
}
func ValidateUsage(p Price, u Usage) error {
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
		if _, e := q(m.Quantity); e != nil {
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
	if e := ValidatePrice(p); e != nil {
		return nil, e
	}
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
		qv, _ := q(m.Quantity)
		if qv.Sign() == 0 {
			continue
		}
		if t.Step != nil {
			s, _ := q(*t.Step)
			qv.Quo(qv, s)
			qv.SetFrac(new(big.Int).Quo(qv.Num(), qv.Denom()), big.NewInt(1))
			qv.Mul(qv, s)
		}
		if t.Minimum != nil {
			min, _ := q(*t.Minimum)
			if qv.Cmp(min) < 0 {
				qv.Set(min)
			}
		}
		n := new(big.Int)
		n.SetString(t.Price.Numerator, 10)
		d := new(big.Int)
		d.SetString(t.Price.Denominator, 10)
		sum.Add(sum, new(big.Rat).Mul(qv, new(big.Rat).SetFrac(n, d)))
	}
	return sum, nil
}

var _ = fmt.Sprintf
