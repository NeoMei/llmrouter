package middleware

import (
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/capability"
	"github.com/gin-gonic/gin"
)

func resolveRequestCapability(c *gin.Context) (capability.Protocol, capability.Operation, error) {
	protocol, operation, err := capability.ResolveRequest(c.Request.Method, c.Request.URL.RequestURI())
	if err != nil {
		return "", "", err
	}
	common.SetContextKey(c, constant.ContextKeyCapabilityProtocol, string(protocol))
	common.SetContextKey(c, constant.ContextKeyCapabilityOperation, string(operation))
	return protocol, operation, nil
}

func channelCapabilityError(channelType int, protocol capability.Protocol, operation capability.Operation) error {
	decision := capability.DefaultRegistry().Resolve(channelType, protocol, operation)
	if decision.Supported {
		return nil
	}
	return fmt.Errorf("channel type %d does not support %s: %s", channelType, operation, decision.Reason)
}

func channelSupportsCapability(channelType int, protocol capability.Protocol, operation capability.Operation) bool {
	return capability.DefaultRegistry().Supports(channelType, protocol, operation)
}
