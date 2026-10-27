package authz

func init() {
	RegisterResource(ResourceDefinition{Resource: "user", LabelKey: "User management", Actions: []ActionDefinition{
		{Action: ActionRead, LabelKey: "Read users", DefaultRoles: []string{BuiltInRoleAdmin}},
		{Action: ActionWrite, LabelKey: "Manage users", DefaultRoles: []string{BuiltInRoleAdmin}},
	}})
	RegisterResource(ResourceDefinition{Resource: "credit", LabelKey: "Product promotional credit", Actions: []ActionDefinition{{Action: "adjust", LabelKey: "Adjust product credit", DefaultRoles: []string{BuiltInRoleAdmin}}}})
	for _, resource := range []string{"subscription", "redemption", "deployment", "billing", "gateway", "status"} {
		RegisterResource(ResourceDefinition{Resource: resource, LabelKey: resource, Actions: []ActionDefinition{{Action: ActionRead, LabelKey: "Read", DefaultRoles: []string{BuiltInRoleAdmin}}, {Action: ActionWrite, LabelKey: "Write", DefaultRoles: []string{BuiltInRoleAdmin}}}})
	}
	for _, resource := range []string{"oauth_provider", "performance", "ratio_sync", "system_task", "system_info"} {
		RegisterResource(ResourceDefinition{Resource: resource, LabelKey: resource, Actions: []ActionDefinition{{Action: ActionRead, LabelKey: "Read"}, {Action: ActionWrite, LabelKey: "Write"}}})
	}
}
