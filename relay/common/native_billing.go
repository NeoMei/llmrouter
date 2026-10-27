package common

import (
	"context"
	"errors"
	"github.com/QuantumNous/new-api/pkg/metering"
	"reflect"
)

// NativeBillingContext is funding-neutral. Local callers attach a journal
// observer; platform callers attach UniHub settlement evidence. The context
// itself never mutates local quota.
type NativeBillingContext struct {
	CallID    string
	AttemptID string
	Price     metering.Price
	Policy    string
	Platform  bool
	Usage     *metering.Usage
	Observe   func(context.Context, metering.Usage) error
}

func (n *NativeBillingContext) Record(ctx context.Context, u metering.Usage) error {
	if n == nil {
		return nil
	}
	// Missing/invalid/incomplete evidence is intentionally forwarded so the
	// owning journal can hold the reservation. Only confirmed evidence is
	// checked against the frozen price here.
	if u.State == "confirmed" {
		if err := metering.ValidateUsage(n.Price, u); err != nil {
			invalid := u
			invalid.State = "invalid"
			if n.Observe != nil {
				if observeErr := n.Observe(ctx, invalid); observeErr != nil {
					return observeErr
				}
			}
			return err
		}
	} else if u.State != "missing" && u.State != "invalid" && u.State != "incomplete" {
		return errors.New("invalid native usage state")
	}
	if n.Usage != nil && n.Usage.State == "confirmed" && !reflect.DeepEqual(*n.Usage, u) {
		if n.Observe != nil {
			if err := n.Observe(ctx, u); err != nil {
				return err
			}
		}
		return ErrNativeUsageConflict
	}
	if n.Observe != nil {
		if err := n.Observe(ctx, u); err != nil {
			return err
		}
	}
	copy := u
	n.Usage = &copy
	return nil
}

var ErrNativeUsageConflict = errors.New("native usage evidence conflict")
