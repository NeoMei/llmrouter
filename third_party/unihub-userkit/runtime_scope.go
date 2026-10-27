package userkit

// RuntimeClientScope identifies the immutable deployment authority associated
// with this service client. Persist it with financial intents before sending.
type RuntimeClientScope struct {
	Origin     string
	ProductID  string
	InstanceID string
}

func (c *RuntimeClient) Scope() RuntimeClientScope {
	return RuntimeClientScope{Origin: c.config.BaseURL, ProductID: c.config.ProductID, InstanceID: c.config.InstanceID}
}
