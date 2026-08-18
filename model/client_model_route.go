package model

// ClientModelRoute is the durable, client-facing identity of a model route.
// Aliases and Metadata are JSON strings so the record remains portable across
// all databases supported by new-api.
type ClientModelRoute struct {
	Id          int    `json:"id" gorm:"primaryKey"`
	PublicId    string `json:"public_id" gorm:"size:191;uniqueIndex;not null"`
	DisplayName string `json:"display_name" gorm:"size:191;not null"`
	Client      string `json:"client" gorm:"size:32;index;not null;default:all"`
	Aliases     string `json:"aliases" gorm:"type:text"`
	Metadata    string `json:"metadata" gorm:"type:text"`
	Enabled     bool   `json:"enabled" gorm:"index;not null;default:true"`
	CreatedTime int64  `json:"created_time"`
	UpdatedTime int64  `json:"updated_time"`
}

func (ClientModelRoute) TableName() string { return "client_model_routes" }

// ClientModelTarget is a channel target for a public route. Groups is a
// runtime-only visibility hint populated from channel/ability data; it is not
// persisted here because group membership remains owned by existing channel
// selection and authorization code.
type ClientModelTarget struct {
	Id                 int    `json:"id" gorm:"primaryKey"`
	RouteId            int    `json:"route_id" gorm:"index;not null"`
	ChannelId          int    `json:"channel_id" gorm:"index;not null"`
	UpstreamModel      string `json:"upstream_model" gorm:"size:191;not null"`
	BillingModel       string `json:"billing_model" gorm:"size:191;not null"`
	PreferredWires     string `json:"preferred_wires" gorm:"type:text;not null"`
	Priority           int    `json:"priority" gorm:"not null;default:0"`
	Weight             int    `json:"weight" gorm:"not null;default:1"`
	Enabled            bool   `json:"enabled" gorm:"index;not null;default:true"`
	ContextWindow      int    `json:"context_window"`
	ContextFitRequired bool   `json:"context_fit_required"`

	Groups        []string                           `json:"-" gorm:"-"`
	GroupPolicies map[string]ClientModelTargetPolicy `json:"-" gorm:"-"`
}

// ClientModelTargetPolicy preserves priority and weight from one ability row.
type ClientModelTargetPolicy struct {
	Priority int
	Weight   int
}

func (ClientModelTarget) TableName() string { return "client_model_targets" }
