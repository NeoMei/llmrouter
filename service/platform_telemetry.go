package service

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/metering"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

type PlatformTelemetryFilter struct {
	Start, End                         int64
	LogType                            int
	Model, TokenName, Group, RequestID string
	UpstreamRequestID                  string
	ChannelID                          int
	ActorID                            string
}

type PlatformTelemetry struct {
	Logs      []*model.Log
	QuotaData []*model.QuotaData
	UserData  []*model.QuotaData
	FlowData  []*model.FlowQuotaData
	Stat      model.Stat
}

// ReadPlatformTelemetry adapts central logical calls to the established product
// monitoring contracts. Local quota remains zero. Central financial amounts
// stay behind UniHub's account financial permissions.
func ReadPlatformTelemetry(ctx context.Context, scope platformbilling.TechnicalScope, userID int, username string, filter PlatformTelemetryFilter) (PlatformTelemetry, error) {
	result := PlatformTelemetry{Logs: make([]*model.Log, 0), QuotaData: make([]*model.QuotaData, 0), UserData: make([]*model.QuotaData, 0), FlowData: make([]*model.FlowQuotaData, 0)}
	if filter.End == 0 {
		filter.End = time.Now().Unix()
	}
	if filter.Start == 0 {
		filter.Start = filter.End - 2592000
	}
	rows, err := platformbilling.ReadTechnicalUsage(ctx, model.DB, scope, filter.Start, filter.End)
	if err != nil {
		return result, err
	}
	var modelPattern *regexp.Regexp
	if strings.Contains(filter.Model, "%") {
		pattern := strings.ReplaceAll(regexp.QuoteMeta(filter.Model), "%", ".*")
		modelPattern, err = regexp.Compile("^" + pattern + "$")
		if err != nil {
			return result, err
		}
	}
	dates := make(map[string]*model.QuotaData)
	users := make(map[string]*model.QuotaData)
	flows := make(map[string]*model.FlowQuotaData)
	for _, row := range rows {
		logType := model.LogTypeSystem
		if row.Outcome == "failed" {
			logType = model.LogTypeError
		} else if row.Outcome == "success" {
			logType = model.LogTypeConsume
		}
		tokenName := "unihub:" + row.Scope.CredentialID
		if scope.AllActors && filter.ActorID != "" && filter.ActorID != row.Scope.ActorID {
			continue
		}
		if filter.LogType != 0 && filter.LogType != logType {
			continue
		}
		if filter.Model != "" && ((modelPattern == nil && filter.Model != row.Model) || modelPattern != nil && !modelPattern.MatchString(row.Model)) {
			continue
		}
		if filter.TokenName != "" && filter.TokenName != tokenName || filter.Group != "" && filter.Group != row.Group {
			continue
		}
		if filter.RequestID != "" && filter.RequestID != row.Scope.CallID || filter.UpstreamRequestID != "" {
			continue
		}
		if filter.ChannelID != 0 && filter.ChannelID != row.ChannelID {
			continue
		}
		other := map[string]any{
			"funding_owner": "unihub", "actor_id": row.Scope.ActorID, "account_id": row.Scope.AccountID, "credential_id": row.Scope.CredentialID,
			"origin_product_id": row.Scope.OriginProductID, "funding_product_id": row.Scope.FundingProductID, "instance_id": row.Scope.InstanceID,
			"call_id": row.Scope.CallID, "task_id": row.Scope.TaskID, "operation": row.Operation, "outcome": row.Outcome,
			"usage_state": row.UsageState, "central_status": row.Status, "central_asset": row.Asset,
			"attempt_count": row.AttemptCount, "latency_ms": row.LatencyMS, "request_count": 1,
		}
		if len(row.Measurements) != 0 {
			other["native_measurements"] = row.Measurements
		}
		name := username
		if scope.AllActors {
			name = row.Scope.ActorID
		}
		result.Logs = append(result.Logs, &model.Log{
			Id: len(result.Logs) + 1, UserId: userID, Username: name, CreatedAt: row.CreatedAt, Type: logType, TokenName: tokenName,
			ModelName: row.Model, PromptTokens: row.PromptTokens, CompletionTokens: row.CompletionTokens, UseTime: int(row.LatencyMS / 1000),
			IsStream: row.IsStream, ChannelId: row.ChannelID, Group: row.Group, RequestId: row.Scope.CallID, Other: common.MapToJsonStr(other),
		})
		if row.CreatedAt >= time.Now().Unix()-60 {
			result.Stat.Rpm++
			result.Stat.Tpm = boundedTechnicalTokens(result.Stat.Tpm, row.PromptTokens, row.CompletionTokens)
		}
		hour := row.CreatedAt - row.CreatedAt%3600
		dateKey := fmt.Sprintf("%d\x00%s", hour, row.Model)
		date := dates[dateKey]
		if date == nil {
			date = &model.QuotaData{UserID: userID, Username: username, ModelName: row.Model, CreatedAt: hour}
			dates[dateKey] = date
			result.QuotaData = append(result.QuotaData, date)
		}
		date.Count++
		date.TokenUsed = boundedTechnicalTokens(date.TokenUsed, row.PromptTokens, row.CompletionTokens)
		userKey := fmt.Sprintf("%d\x00%s", hour, row.Scope.ActorID)
		user := users[userKey]
		if user == nil {
			user = &model.QuotaData{UserID: userID, Username: row.Scope.ActorID, CreatedAt: hour}
			users[userKey] = user
			result.UserData = append(result.UserData, user)
		}
		user.Count++
		user.TokenUsed = boundedTechnicalTokens(user.TokenUsed, row.PromptTokens, row.CompletionTokens)
		flowKey := row.Scope.ActorID + "\x00" + row.Scope.CredentialID + "\x00" + row.Group + "\x00" + row.Model
		flow := flows[flowKey]
		if flow == nil {
			flow = &model.FlowQuotaData{UserID: userID, Username: name, TokenName: tokenName, UseGroup: row.Group, ModelName: row.Model}
			flows[flowKey] = flow
			result.FlowData = append(result.FlowData, flow)
		}
		flow.Count++
		flow.TokenUsed = boundedTechnicalTokens(flow.TokenUsed, row.PromptTokens, row.CompletionTokens)
	}
	return result, nil
}

func boundedTechnicalTokens(current int, counts ...int) int {
	for _, count := range counts {
		if count > common.MaxQuota-current {
			return common.MaxQuota
		}
		current += count
	}
	return current
}

func recordPlatformRelayTelemetry(ctx *gin.Context, info *relaycommon.RelayInfo) {
	if ctx == nil || info == nil || info.PlatformBilling == nil {
		return
	}
	recordPlatformRelayTelemetryInfo(context.WithoutCancel(ctx.Request.Context()), info)
}

func recordPlatformRelayTelemetryInfo(ctx context.Context, info *relaycommon.RelayInfo) {
	if info == nil || info.PlatformBilling == nil {
		return
	}
	latency := time.Since(info.StartTime).Milliseconds()
	if info.StartTime.IsZero() || latency < 0 {
		latency = 0
	}
	recorded, err := info.PlatformBilling.RecordTelemetry(ctx, info.UsingGroup, latency, info.IsStream)
	if err != nil {
		logger.LogError(ctx, "platform technical telemetry: "+err.Error())
		return
	}
	if recorded {
		output := int64(0)
		if trusted := info.PlatformBilling.ConfirmedTokenUsage(); trusted != nil {
			output = int64(trusted.CompletionTokens)
		} else if info.NativeBilling != nil {
			output = nativeOutputTokens(info.NativeBilling.Usage)
		}
		perfmetrics.RecordRelaySampleAsync(info, true, output)
	}
}

func nativeOutputTokens(usage *metering.Usage) int64 {
	if usage == nil || usage.State != "confirmed" {
		return 0
	}
	output := int64(0)
	for _, measurement := range usage.Measurements {
		if measurement.Unit != "token" || measurement.Quantity.Scale != 0 || (measurement.Key != "output" && measurement.Key != "reasoning") {
			continue
		}
		count, err := strconv.ParseInt(measurement.Quantity.Value, 10, 32)
		if err != nil || count < 0 {
			continue
		}
		if output > common.MaxQuota-count {
			return common.MaxQuota
		}
		output += count
	}
	return output
}
