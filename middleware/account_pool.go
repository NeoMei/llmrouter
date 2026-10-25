package middleware

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/capability"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/accountpool"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// SetupAvailableAccountChannel skips exhausted pools without spending sends.
// Every alternative goes through the existing group/model/path authorization
// selector; the bounded scan never revisits an exhausted channel.
func SetupAvailableAccountChannel(c *gin.Context, channel *model.Channel, modelName string, params *service.RetryParam) (*model.Channel, *types.NewAPIError) {
	setupErr := SetupContextForSelectedChannel(c, channel, modelName)
	if setupErr == nil {
		return channel, nil
	}
	return FallbackAccountChannel(c, channel, modelName, params, setupErr)
}

// FallbackAccountChannel also handles late Gateway account exhaustion, which
// becomes known only after the adapter resolves its provider profile.
func FallbackAccountChannel(c *gin.Context, channel *model.Channel, modelName string, params *service.RetryParam, setupErr *types.NewAPIError) (*model.Channel, *types.NewAPIError) {
	var unavailable *model.UpstreamAccountUnavailable
	if !errors.As(setupErr.Err, &unavailable) {
		return nil, setupErr
	}
	earliest := unavailable.RetryAt
	if _, specific := common.GetContextKey(c, constant.ContextKeyTokenSpecificChannelId); specific {
		return nil, setupErr
	}
	var count int64
	if err := model.DB.Model(&model.Channel{}).Count(&count).Error; err != nil {
		return nil, types.NewError(err, types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}
	scanParams := *params
	scanParams.Retry = common.GetPointer(params.GetRetry())
	if params.ExcludedChannels == nil {
		params.ExcludedChannels = map[int]bool{}
	}
	params.ExcludedChannels[channel.Id] = true
	scanParams.ExcludedChannels = params.ExcludedChannels
	if scanParams.Operation == "" {
		scanParams.Protocol, scanParams.Operation, _ = capability.ResolveRequest(c.Request.Method, c.Request.URL.Path)
	}
	for scan := int64(0); scan < count*2; scan++ {
		next, group, err := service.CacheGetRandomSatisfiedChannel(&scanParams)
		if err != nil {
			return nil, types.NewError(err, types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
		}
		if next == nil {
			scanParams.IncreaseRetry()
			continue
		}
		scanParams.ExcludedChannels[next.Id] = true
		setupErr = SetupContextForSelectedChannel(c, next, modelName)
		if setupErr == nil {
			common.SetContextKey(c, constant.ContextKeyUsingGroup, group)
			c.Header("Retry-After", "")
			return next, nil
		}
		if !errors.As(setupErr.Err, &unavailable) {
			return nil, setupErr
		}
		if !unavailable.RetryAt.IsZero() && (earliest.IsZero() || unavailable.RetryAt.Before(earliest)) {
			earliest = unavailable.RetryAt
		}
	}
	accountpool.SetUnavailableHeader(c, &model.UpstreamAccountUnavailable{RetryAt: earliest})
	return nil, setupErr
}
