package model

import "context"

// Payment confirmation and spendable-credit delivery are separate receipts.
// Enrich the original history without turning a read into payment/reward retry.
func PopulateProductTopUpCreditStatuses(ctx context.Context, topups []*TopUp) error {
	if len(topups) == 0 || !DB.Migrator().HasTable(&ProductCreditAward{}) {
		return nil
	}
	byID := map[string]*TopUp{}
	ids := make([]string, 0, len(topups))
	for _, t := range topups {
		if t == nil {
			continue
		}
		id := productDigest("payment:" + productDigest(t.PaymentProvider+":"+t.TradeNo))
		byID[id] = t
		ids = append(ids, id)
	}
	var awards []ProductCreditAward
	if e := DB.WithContext(ctx).Select("id", "state").Where("id IN ?", ids).Find(&awards).Error; e != nil {
		return e
	}
	for _, a := range awards {
		if t := byID[a.ID]; t != nil {
			t.CreditStatus = a.State
		}
	}
	return nil
}
