package authz

func init() {
	RegisterResource(ResourceDefinition{Resource: "product", LabelKey: "Product application", Actions: []ActionDefinition{{Action: "use", LabelKey: "Use product application", DescriptionKey: "View own product activity and the model catalog."}}})
	for _, resource := range []struct {
		name, label string
		write       bool
	}{{"catalog", "Model catalog", true}, {"log", "Technical logs", false}, {"routing", "Client model routing", true}, {"settings", "Product runtime settings", true}} {
		roles := []string{BuiltInRoleAdmin}
		if resource.name == "settings" {
			roles = nil
		}
		actions := []ActionDefinition{{Action: ActionRead, LabelKey: "Read", DescriptionKey: "Read product operational data.", DefaultRoles: roles}}
		if resource.write {
			actions = append(actions, ActionDefinition{Action: ActionWrite, LabelKey: "Write", DescriptionKey: "Change product operational configuration.", DefaultRoles: roles})
		}
		RegisterResource(ResourceDefinition{Resource: resource.name, LabelKey: resource.label, Actions: actions})
	}
}
