package controller

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
)

// The authenticated product principal owns self scope. Query parameters can
// narrow display filters but cannot substitute an actor or elevate to accounts.
func productTelemetry(c *gin.Context, allActors bool) (service.PlatformTelemetry, bool, bool) {
	p, projected := authz.ProductPrincipal(c)
	if !projected {
		// UserAuth establishes native PAT provenance against the current owner
		// and credential. A PAT may read native telemetry without becoming a
		// browser session or gaining session-management authority.
		if middleware.NativeRealmAuthenticated(c) {
			if c.GetBool("use_access_token") {
				identity, ok := middleware.GetAuthIdentity(c)
				if ok && identity.UserID > 0 && identity.UserID == c.GetInt("id") && identity.UserAuthVersion > 0 && identity.SessionID == "" {
					return service.PlatformTelemetry{}, false, false
				}
			} else if identity, ok := middleware.GetSessionAuthIdentity(c); ok {
				native, err := service.IsNativeLoginSession(c.Request.Context(), identity)
				if err == nil && native {
					return service.PlatformTelemetry{}, false, false
				}
			}
		}
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return service.PlatformTelemetry{}, true, false
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
	// Product principals have no trustworthy realm attribution for legacy
	// local rows. Only native authentication may read those rows through the
	// native handler; product telemetry is sourced solely from its journal.
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
	if view == "flow" {
		common.ApiSuccess(c, telemetry.FlowData)
	} else if view == "users" {
		common.ApiSuccess(c, telemetry.UserData)
	} else {
		common.ApiSuccess(c, telemetry.QuotaData)
	}
	return true
}
