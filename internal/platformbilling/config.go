package platformbilling

import (
	"github.com/QuantumNous/new-api/common"
	"strconv"
)

// ConfigFromEnv takes an injected environment reader. No credentials or service
// clients are process globals; enabling a deployment requires explicit limits.
func ConfigFromEnv(get func(string) string) (Config, error) {
	c := Config{Enabled: get("UNIHUB_PLATFORM_ENABLED") == "true"}
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
	return c, nil
}
