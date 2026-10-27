package userkit

import (
	"context"
	"net/url"
)

type RuntimeRefundRequest struct {
	RequestKey string `json:"request_key"`
	Amount     string `json:"amount"`
	Reason     string `json:"reason"`
}

func (c *RuntimeClient) Refund(ctx context.Context, id string, in RuntimeRefundRequest) (out RuntimeAdjustment, e error) {
	e = c.request(ctx, "POST", "/calls/"+url.PathEscape(id)+"/refund", "", in, &out)
	if e == nil {
		e = c.scope(out.ProductID, out.InstanceID)
	}
	return
}
