package controller

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
)

// The authenticated product principal owns self scope. Query parameters can
// narrow display filters but cannot substitute an actor or elevate to accounts.
func productTelemetry(c *gin.Context, allActors bool) (service.PlatformTelemetry, bool, bool) {
	p, projected := authz.ProductPrincipal(c)
	if !projected {
		return service.PlatformTelemetry{}, false, false
	}
	config, err := platformaccount.Current()
	if err != nil || (!config.Enabled && !platformaccount.RuntimeEnabled()) || !p.Access.Active || p.Access.ExpiresAt <= time.Now().Unix() || p.Access.Subject == "" || p.Access.Issuer != config.PublicOrigin || p.Access.ProductID != config.ProductID || p.Access.InstanceID != config.InstanceID {
		c.AbortWithStatus(http.StatusUnauthorized)
		return service.PlatformTelemetry{}, true, false
	}
	if allActors && !authz.CanRequest(c, authz.Permission{Resource: "log", Action: "read"}) {
		c.AbortWithStatus(http.StatusForbidden)
		return service.PlatformTelemetry{}, true, false
	}
	start, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	end, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	logType, _ := strconv.Atoi(c.Query("type"))
	channel, _ := strconv.Atoi(c.Query("channel"))
	userID, username := c.GetInt("id"), c.GetString("username")
	if allActors {
		userID, username = 0, ""
	}
	result, err := service.ReadPlatformTelemetry(c.Request.Context(), platformbilling.TechnicalScope{ActorID: p.Access.Subject, InstanceID: p.Access.InstanceID, AllActors: allActors}, userID, username, service.PlatformTelemetryFilter{
		Start: start, End: end, LogType: logType, ChannelID: channel, Model: c.Query("model_name"), TokenName: c.Query("token_name"), Group: c.Query("group"),
		RequestID: c.Query("request_id"), UpstreamRequestID: c.Query("upstream_request_id"),
		ActorID: c.Query("username"),
	})
	if err != nil {
		common.ApiError(c, err)
		return service.PlatformTelemetry{}, true, false
	}
	return result, true, true
}

func serveProductTelemetryLogs(c *gin.Context, allActors bool) bool {
	telemetry, projected, ok := productTelemetry(c, allActors)
	if !projected || !ok {
		return projected
	}
	logs := telemetry.Logs
	// Management and login events still live in LogDB; retain that history while
	// platform model calls are read directly from their authoritative journal.
	if model.LOG_DB != nil && model.LOG_DB.Migrator().HasTable(&model.Log{}) {
		start, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
		end, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
		kind, _ := strconv.Atoi(c.Query("type"))
		var local []*model.Log
		var err error
		if allActors {
			channel, _ := strconv.Atoi(c.Query("channel"))
			local, _, err = model.GetAllLogs(kind, start, end, c.Query("model_name"), c.Query("username"), c.Query("token_name"), 0, 10000, channel, c.Query("group"), c.Query("request_id"), c.Query("upstream_request_id"))
		} else {
			local, _, err = model.GetUserLogs(c.GetInt("id"), kind, start, end, c.Query("model_name"), c.Query("token_name"), 0, 10000, c.Query("group"), c.Query("request_id"), c.Query("upstream_request_id"))
		}
		if err != nil {
			common.ApiError(c, err)
			return true
		}
		centralIDs := make(map[string]bool)
		for _, log := range logs {
			centralIDs[log.RequestId] = true
		}
		for _, log := range local {
			if centralIDs[log.RequestId] {
				continue
			}
			log.Content, log.Ip, log.UpstreamRequestId = "", "", ""
			log.Quota = 0
			log.Other = common.MapToJsonStr(map[string]any{"financial_details": false})
			logs = append(logs, log)
		}
	}
	sort.SliceStable(logs, func(i, j int) bool {
		if logs[i].CreatedAt == logs[j].CreatedAt {
			return logs[i].RequestId > logs[j].RequestId
		}
		return logs[i].CreatedAt > logs[j].CreatedAt
	})
	page := common.GetPageQuery(c)
	page.SetTotal(len(logs))
	start, end := page.GetStartIdx(), page.GetEndIdx()
	if start < 0 || start > len(logs) {
		start = len(logs)
	}
	if end < start || end > len(logs) {
		end = len(logs)
	}
	for i := start; i < end; i++ {
		logs[i].Id = i + 1
	}
	page.SetItems(logs[start:end])
	common.ApiSuccess(c, page)
	return true
}

func serveProductQuotaTelemetry(c *gin.Context, allActors bool, view string) bool {
	telemetry, projected, ok := productTelemetry(c, allActors)
	if !projected || !ok {
		return projected
	}
	if !model.DB.Migrator().HasTable(&model.QuotaData{}) {
		if view == "flow" {
			common.ApiSuccess(c, telemetry.FlowData)
		} else if view == "users" {
			common.ApiSuccess(c, telemetry.UserData)
		} else {
			common.ApiSuccess(c, telemetry.QuotaData)
		}
		return true
	}
	start, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	end, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	if end == 0 {
		end = time.Now().Unix()
	}
	if start == 0 {
		start = end - 2592000
	}
	if view == "flow" {
		userID, role := c.GetInt("id"), common.RoleCommonUser
		if allActors {
			userID, role = 0, common.RoleAdminUser
		}
		local, err := model.GetFlowQuotaData(start, end, c.Query("username"), userID, role)
		if err != nil {
			common.ApiError(c, err)
			return true
		}
		for _, row := range local {
			row.Quota = 0
		}
		common.ApiSuccess(c, append(telemetry.FlowData, local...))
		return true
	}
	var local []*model.QuotaData
	var err error
	switch {
	case view == "users":
		local, err = model.GetQuotaDataGroupByUser(start, end)
		telemetry.QuotaData = telemetry.UserData
	case allActors:
		local, err = model.GetAllQuotaDates(start, end, c.Query("username"))
	default:
		local, err = model.GetQuotaDataByUserId(c.GetInt("id"), start, end)
	}
	if err != nil {
		common.ApiError(c, err)
		return true
	}
	for _, row := range local {
		row.Quota = 0
	}
	common.ApiSuccess(c, append(telemetry.QuotaData, local...))
	return true
}
