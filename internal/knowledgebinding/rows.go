package knowledgebinding

type KnowledgeActiveBinding struct {
	InstanceID      string `gorm:"size:64;primaryKey"`
	OriginProductID string `gorm:"size:32;primaryKey"`
	Model           string `gorm:"size:64;primaryKey"`
	WireOperation   string `gorm:"size:32;primaryKey"`
	RouteID         int
	RouteGeneration string `gorm:"size:64"`
	ActivePin       string `gorm:"size:64"`
}

func (KnowledgeActiveBinding) TableName() string { return "knowledge_active_bindings" }

type KnowledgeRoutePin struct {
	Pin             string `gorm:"size:64;primaryKey"`
	InstanceID      string `gorm:"size:64;index:knowledge_pin_scope"`
	OriginProductID string `gorm:"size:32;index:knowledge_pin_scope"`
	Model           string `gorm:"size:64;index:knowledge_pin_scope"`
	WireOperation   string `gorm:"size:32;index:knowledge_pin_scope"`
	RouteID         int
	TargetID        int
	ChannelID       int
	RouteGeneration string `gorm:"size:64"`
	Fingerprint     string `gorm:"size:64"`
}

func (KnowledgeRoutePin) TableName() string { return "knowledge_route_pins" }
