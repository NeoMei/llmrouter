package platformbilling

import (
	"github.com/QuantumNous/new-api/common"
	"strconv"
	"strings"
)

// ConfigFromEnv takes an injected environment reader. No credentials or service
// clients are process globals; enabling a deployment requires explicit limits.
func ConfigFromEnv(get func(string) string) (Config, error) {
	c := Config{Enabled: get("UNIHUB_PLATFORM_ENABLED") == "true"}
	c.InstanceID = strings.TrimSpace(get("UNIHUB_PRODUCT_INSTANCE_ID"))
	if c.InstanceID == "" {
		c.InstanceID = "default"
	}
	if !c.Enabled {
		return c, nil
	}
	c.Origin = get("UNIHUB_ORIGIN")
	c.ServiceToken = get("UNIHUB_MODEL_SERVICE_TOKEN")
	c.Group = get("UNIHUB_ROUTING_GROUP")
	var err error
	c.PrincipalID, err = strconv.Atoi(get("UNIHUB_ROUTING_PRINCIPAL_ID"))
	if err != nil || c.PrincipalID < 1 {
		return c, ErrInvalid
	}
	c.MaxInputTokens, err = strconv.Atoi(get("UNIHUB_MAX_INPUT_TOKENS"))
	if err != nil {
		return c, ErrInvalid
	}
	c.MaxOutputTokens, err = strconv.Atoi(get("UNIHUB_MAX_OUTPUT_TOKENS"))
	if err != nil {
		return c, ErrInvalid
	}
	c.MaxBodyBytes, err = strconv.ParseInt(get("UNIHUB_MAX_BODY_BYTES"), 10, 64)
	if err != nil {
		return c, ErrInvalid
	}
	if raw := get("UNIHUB_MODEL_RATE_IDS"); raw != "" {
		if common.UnmarshalJsonStr(raw, &c.RateIDs) != nil {
			return c, ErrInvalid
		}
	}
	// Legacy model pins select CREDIT rates only. Explicit asset pins take
	// precedence, for example {"CNY/6":{"model-name":"cash-rate-id"}}.
	if raw := get("UNIHUB_MODEL_ASSET_RATE_IDS"); raw != "" {
		if common.UnmarshalJsonStr(raw, &c.AssetRateIDs) != nil {
			return c, ErrInvalid
		}
		for asset := range c.AssetRateIDs {
			if asset != "CREDIT/6" && asset != "CNY/6" {
				return c, ErrInvalid
			}
		}
	}
	// Explicit operation pins opt requests into metered_v2. Duration and media
	// caps are deployment-owned bounds, never values trusted from client JSON.
	if raw := get("UNIHUB_OPERATION_RATE_IDS"); raw != "" {
		if common.UnmarshalJsonStr(raw, &c.OperationRateIDs) != nil {
			return c, ErrInvalid
		}
	}
	if raw := get("UNIHUB_OPERATION_LIMITS"); raw != "" {
		if common.UnmarshalJsonStr(raw, &c.OperationLimits) != nil {
			return c, ErrInvalid
		}
		for _, models := range c.OperationLimits {
			for _, limit := range models {
				if limit.MaxBodyBytes < 0 || limit.MaxBodyBytes > 64<<20 || limit.MaxQuantity < 0 || limit.MaxQuantity > 86400 {
					return c, ErrInvalid
				}
			}
		}
	}
	return c, nil
}
