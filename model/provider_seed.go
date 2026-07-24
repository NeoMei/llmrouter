package model

// provider_seed.go — idempotent, cross-DB seed of the 4 codex-gateway provider
// profiles and their models. Mirrors migrations/002_seed_providers.sql.
//
// QuirkRules is stored as a JSON string (ProviderModel.QuirkRules is TEXT in
// the GORM model; the PG table is JSONB and accepts valid-JSON text via an
// implicit cast, so the same string works on SQLite/MySQL/PostgreSQL).
//
// Wired into migrateDB / migrateDBFast alongside the other Initialize* hooks,
// so the catalog is present on every fresh bring-up without a manual psql step.

// seedProviderCatalog is the single source of truth for the seeded catalog.
var seedProviderCatalog = []struct {
	Profile ProviderProfile
	Models  []ProviderModel
}{
	{
		Profile: ProviderProfile{
			Name:        "deepseek",
			DisplayName: "DeepSeek",
			BaseURL:     "https://api.deepseek.com",
			AuthMode:    "api_key",
			Status:      "active",
		},
		Models: []ProviderModel{
			{
				ModelId:       "deepseek-v4-flash",
				DisplayName:   "DeepSeek V4 Flash",
				ContextWindow: 131072,
				Status:        "active",
				QuirkRules:    `{"reasoning_mode": "effort", "preserve_reasoning": true}`,
			},
			{
				ModelId:       "deepseek-v4-pro",
				DisplayName:   "DeepSeek V4 Pro",
				ContextWindow: 131072,
				Status:        "active",
				QuirkRules:    `{"reasoning_mode": "effort", "preserve_reasoning": true}`,
			},
		},
	},
	{
		Profile: ProviderProfile{
			Name:        "kimi",
			DisplayName: "Kimi Coding Plan",
			BaseURL:     "https://api.kimi.com/coding/v1",
			AuthMode:    "api_key",
			Status:      "active",
		},
		Models: []ProviderModel{
			{
				ModelId:       "kimi-for-coding",
				DisplayName:   "Kimi for Coding",
				ContextWindow: 262144,
				Status:        "active",
				QuirkRules:    `{"reasoning_mode": "effort", "preserve_reasoning": true, "strip_params": ["temperature", "top_p", "presence_penalty", "frequency_penalty"], "tool_choice": "auto_only"}`,
			},
			{
				ModelId:       "k3",
				DisplayName:   "Kimi K3",
				ContextWindow: 262144,
				Status:        "active",
				QuirkRules:    `{"reasoning_mode": "effort", "preserve_reasoning": true, "strip_params": ["temperature", "top_p", "presence_penalty", "frequency_penalty"], "effort_map": {"none": "none", "low": "low", "medium": "high", "high": "high", "xhigh": "max", "max": "max"}}`,
			},
		},
	},
	{
		Profile: ProviderProfile{
			Name:        "zhipu",
			DisplayName: "Zhipu Coding Plan",
			BaseURL:     "https://open.bigmodel.cn/api/coding/paas/v4",
			AuthMode:    "api_key",
			Status:      "active",
		},
		Models: []ProviderModel{
			{
				ModelId:       "glm-5.2",
				DisplayName:   "GLM-5.2",
				ContextWindow: 131072,
				Status:        "active",
				QuirkRules:    `{"reasoning_mode": "effort", "preserve_reasoning": true, "bracket_strip": true}`,
			},
		},
	},
	{
		Profile: ProviderProfile{
			Name:        "alibaba",
			DisplayName: "Alibaba Coding Plan",
			BaseURL:     "https://coding.dashscope.aliyuncs.com/v1",
			AuthMode:    "api_key",
			Status:      "active",
		},
		Models: []ProviderModel{
			{
				ModelId:       "qwen3-coder-next",
				DisplayName:   "Qwen3 Coder Next",
				ContextWindow: 131072,
				Status:        "active",
				QuirkRules:    `{"reasoning_mode": "effort", "preserve_reasoning": true}`,
			},
		},
	},
}

// SeedProviderProfiles idempotently inserts the built-in provider catalog.
// Existing rows (matched by profile name, and by profile+model_id) are left
// untouched, so operator-edited quirks/URLs survive restarts. Must run after
// the provider tables have been migrated.
func SeedProviderProfiles() error {
	for _, p := range seedProviderCatalog {
		var profile ProviderProfile
		if err := DB.Where(ProviderProfile{Name: p.Profile.Name}).
			FirstOrCreate(&profile, p.Profile).Error; err != nil {
			return err
		}
		if profile.Id == 0 {
			continue
		}
		for _, m := range p.Models {
			m.ProfileId = profile.Id
			var existing ProviderModel
			if err := DB.Where("profile_id = ? AND model_id = ?", profile.Id, m.ModelId).
				FirstOrCreate(&existing, m).Error; err != nil {
				return err
			}
		}
	}
	return nil
}
