package controller

import (
	"errors"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/capability"
	"github.com/QuantumNous/new-api/relay/channel/gateway"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type accountPoolScope struct {
	Operation string `json:"operation"`
	Model     string `json:"model"`
}
type accountPoolRow struct {
	MemberID              string                 `json:"member_id"`
	GenerationID          string                 `json:"generation_id,omitempty"`
	Indices               []int                  `json:"indices"`
	LegacyDisabledIndices []int                  `json:"legacy_disabled_indices,omitempty"`
	QuotaGroup            string                 `json:"quota_group"`
	State                 string                 `json:"state"`
	Paused                bool                   `json:"paused"`
	Isolated              bool                   `json:"isolated"`
	LegacyBlocked         bool                   `json:"legacy_blocked"`
	RetryAt               *time.Time             `json:"retry_at,omitempty"`
	Blockers              []model.AccountBlocker `json:"blockers"`
}
type accountPoolSnapshot struct {
	ServerTime     time.Time        `json:"server_time"`
	ChannelID      int              `json:"channel_id"`
	ProfileID      int              `json:"profile_id,omitempty"`
	PoolID         string           `json:"pool_id"`
	SelectedScope  accountPoolScope `json:"selected_scope"`
	QuotaRemaining string           `json:"quota_remaining"`
	GroupScope     string           `json:"group_scope"`
	Status         string           `json:"status"`
	Counts         map[string]int   `json:"counts"`
	RetryAt        *time.Time       `json:"retry_at,omitempty"`
	Members        []accountPoolRow `json:"members"`
}
type accountPoolMutation struct {
	Operation          string  `json:"operation"`
	Model              string  `json:"model"`
	ProfileID          int     `json:"profile_id"`
	PoolID             string  `json:"pool_id"`
	MemberID           string  `json:"member_id"`
	GenerationID       string  `json:"generation_id"`
	TargetScope        string  `json:"target_scope"`
	QuotaGroup         *string `json:"quota_group"`
	ExpectedQuotaGroup *string `json:"expected_quota_group"`
}

func validateAccountPoolScope(operation, modelName string) error {
	if modelName == "" || len(modelName) > 512 || strings.TrimSpace(modelName) != modelName || strings.ContainsAny(modelName, "\x00\r\n") {
		return errors.New("invalid_scope")
	}
	for _, op := range capability.AllOperations() {
		if operation == string(op) {
			return nil
		}
	}
	return errors.New("invalid_scope")
}

// resolveAccountPool reloads only current channel-authorized identities. No
// reference, row index, or credential authority is accepted from the browser.
func resolveAccountPool(c *gin.Context, db *gorm.DB, ch *model.Channel, profileID int, scope accountPoolScope, now time.Time) ([]model.AccountManagementMember, int, error) {
	authorized := false
	mapping := map[string]string{}
	if raw := ch.GetModelMapping(); raw != "" && raw != "{}" {
		if err := common.UnmarshalJsonStr(raw, &mapping); err != nil {
			return nil, 0, errors.New("configuration_unavailable")
		}
	}
	for _, name := range ch.GetModels() {
		upstream := name
		if mapped, ok := mapping[name]; ok && mapped != "" {
			upstream = mapped
		}
		// Retain the existing one-hop catalog scope and add the legacy relay
		// chain terminal. Invalid cycles never authorize a legacy terminal.
		terminal, err := common.ResolveModelMapping(name, mapping)
		if upstream == scope.Model || (err == nil && terminal == scope.Model) {
			authorized = true
			break
		}
	}
	if !authorized {
		return nil, 0, errors.New("model_not_in_channel")
	}
	if ch.Type != constant.ChannelTypeGateway {
		if profileID != 0 {
			return nil, 0, errors.New("wrong_profile")
		}
		rows, err := model.ChannelAccountManagement(c.Request.Context(), db, ch)
		return rows, 0, err
	}
	pm, pp, err := model.AccountManagementProfile(c.Request.Context(), db, scope.Model)
	if err != nil {
		return nil, 0, errors.New("configuration_unavailable")
	}
	if profileID != 0 && profileID != pm.ProfileId {
		return nil, 0, errors.New("wrong_profile")
	}
	rows, err := gateway.GlobalKeySel.ManagementAccounts(c.Request.Context(), db, pp.Id, ch.Id, now)
	if err != nil {
		return nil, 0, err
	}
	if gateway.GlobalLoader == nil {
		for i := range rows {
			rows[i].Barriers = append(rows[i].Barriers, model.AccountBlocker{Reason: "configuration_unavailable", Scope: "pool"})
		}
	}
	if pm.Status != "active" || pp.Status != "active" {
		for i := range rows {
			rows[i].Barriers = append(rows[i].Barriers, model.AccountBlocker{Reason: "profile_inactive", Scope: "pool"})
		}
	}
	return rows, pp.Id, nil
}

func GetAccountPool(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		accountPoolError(c, "invalid_channel")
		return
	}
	scope := accountPoolScope{Operation: c.Query("operation"), Model: c.Query("model")}
	if err = validateAccountPoolScope(scope.Operation, scope.Model); err != nil {
		accountPoolError(c, err.Error())
		return
	}
	profileID := 0
	if v := c.Query("profile_id"); v != "" {
		profileID, err = strconv.Atoi(v)
		if err != nil || profileID <= 0 {
			accountPoolError(c, "wrong_profile")
			return
		}
	}
	now := time.Now().UTC()
	snapshot := accountPoolSnapshot{ServerTime: now, ChannelID: id, SelectedScope: scope, QuotaRemaining: "unknown", GroupScope: "global", Status: "ready", Counts: map[string]int{"total": 0, "available": 0, "cooling": 0, "isolated": 0, "paused": 0, "legacy_blocked": 0, "unavailable": 0}, Members: []accountPoolRow{}}
	err = model.WithAccountManagementChannel(c.Request.Context(), model.DB, id, func(tx *gorm.DB, ch *model.Channel) error {
		rows, pid, err := resolveAccountPool(c, tx, ch, profileID, scope, now)
		if err != nil {
			return err
		}
		snapshot.ProfileID = pid
		snapshot.PoolID = "channel:" + strconv.Itoa(id)
		if pid > 0 {
			snapshot.PoolID = "gateway:" + strconv.Itoa(pid)
		}
		if len(rows) > 2000 {
			return errors.New("pool_too_large")
		}
		refs := make([]model.UpstreamAccountRef, 0, len(rows)*2)
		for _, row := range rows {
			refs = append(refs, row.Ref)
			if row.Health.MemberID != "" && row.Health.MemberID != row.Ref.MemberID {
				refs = append(refs, row.Health)
			}
		}
		health, err := model.ProjectUpstreamAccounts(c.Request.Context(), tx, refs, model.UpstreamAccountScope{Operation: scope.Operation, Model: scope.Model}, now)
		if err != nil {
			return err
		}
		byID := map[string]model.AccountHealth{}
		for _, h := range health {
			byID[h.MemberID] = h
		}
		for _, row := range rows {
			item := accountPoolRow{MemberID: row.Ref.MemberID, GenerationID: row.Health.MemberID, Indices: row.Indices, LegacyDisabledIndices: row.LegacyDisabledIndices, QuotaGroup: byID[row.Ref.MemberID].QuotaGroup, State: "available", Blockers: []model.AccountBlocker{}}
			item.Blockers = append(item.Blockers, row.Barriers...)
			item.Blockers = append(item.Blockers, byID[row.Ref.MemberID].Blockers...)
			if row.Health.MemberID != row.Ref.MemberID {
				item.Blockers = append(item.Blockers, byID[row.Health.MemberID].Blockers...)
			}
			if ch.Status != common.ChannelStatusEnabled {
				item.Blockers = append(item.Blockers, model.AccountBlocker{Reason: "channel_disabled", Scope: "pool"})
			}
			permanent, unavailable := false, false
			seen := map[model.AccountBlocker]bool{}
			blockers := item.Blockers
			item.Blockers = []model.AccountBlocker{}
			for _, b := range blockers {
				// Group blockers can appear in both owner and generation; collapse them.
				identity := b
				identity.RetryAt = nil
				if b.Scope == "quota_group_global" && seen[identity] {
					continue
				}
				seen[identity] = true
				item.Blockers = append(item.Blockers, b)
				if b.Reason == "manual_pause" {
					item.Paused = true
				}
				if b.Reason == "invalid_credential" {
					item.Isolated = true
				}
				if strings.HasPrefix(b.Reason, "legacy_") || b.Reason == "channel_disabled" || b.Reason == "profile_inactive" {
					item.LegacyBlocked = true
				}
				if b.Reason == "credential_unavailable" || b.Reason == "configuration_unavailable" {
					unavailable = true
				}
				if b.RetryAt == nil {
					permanent = true
				} else if item.RetryAt == nil || b.RetryAt.After(*item.RetryAt) {
					item.RetryAt = b.RetryAt
				}
			}
			switch {
			case item.Paused:
				item.State = "paused"
			case item.Isolated:
				item.State = "isolated"
			case unavailable:
				item.State = "unavailable"
			case permanent:
				item.State = "legacy_blocked"
			case item.RetryAt != nil:
				item.State = "cooling"
			}
			if permanent {
				item.RetryAt = nil
			}
			snapshot.Counts["total"]++
			snapshot.Counts[item.State]++
			if item.RetryAt != nil && (snapshot.RetryAt == nil || item.RetryAt.Before(*snapshot.RetryAt)) {
				snapshot.RetryAt = item.RetryAt
			}
			snapshot.Members = append(snapshot.Members, item)
		}
		if len(rows) == 0 {
			snapshot.Status = "empty"
			if ch.Type == constant.ChannelTypeGateway && (gateway.GlobalKeySel == nil || gateway.GlobalLoader == nil) {
				snapshot.Status = "configuration_unavailable"
			}
			if ch.Type != constant.ChannelTypeGateway {
				snapshot.Status = "credential_unavailable"
			}
		}
		if snapshot.Counts["available"] > 0 {
			snapshot.RetryAt = nil
		}
		return nil
	})
	if err != nil {
		accountPoolError(c, accountPoolErrorCode(err))
		return
	}
	common.ApiSuccess(c, snapshot)
}

func MutateAccountPool(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		accountPoolError(c, "invalid_channel")
		return
	}
	var req accountPoolMutation
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
	if err = common.DecodeJson(c.Request.Body, &req); err != nil {
		accountPoolError(c, "invalid_request")
		return
	}
	if err = validateAccountPoolScope(req.Operation, req.Model); err != nil {
		accountPoolError(c, err.Error())
		return
	}
	parsed, err := uuid.Parse(req.MemberID)
	if err != nil || parsed.String() != req.MemberID {
		accountPoolError(c, "invalid_member")
		return
	}
	action := path.Base(c.FullPath())
	if req.TargetScope != "selected" && req.TargetScope != "operation" && req.TargetScope != "model" && req.TargetScope != "account_global" {
		accountPoolError(c, "invalid_scope")
		return
	}
	scope := model.UpstreamAccountScope{}
	if req.TargetScope == "selected" || req.TargetScope == "operation" {
		scope.Operation = req.Operation
	}
	if req.TargetScope == "selected" || req.TargetScope == "model" {
		scope.Model = req.Model
	}
	now := time.Now().UTC()
	profileID := 0
	err = model.WithAccountManagementChannel(c.Request.Context(), model.DB, id, func(tx *gorm.DB, ch *model.Channel) error {
		rows, pid, err := resolveAccountPool(c, tx, ch, req.ProfileID, accountPoolScope{Operation: req.Operation, Model: req.Model}, now)
		if err != nil {
			return err
		}
		profileID = pid
		if pid != req.ProfileID {
			return errors.New("wrong_profile")
		}
		for _, row := range rows {
			if row.Ref.MemberID != req.MemberID {
				continue
			}
			if row.Ref.PoolID != req.PoolID {
				return errors.New("wrong_pool")
			}
			switch action {
			case "pause", "resume", "quota-group":
				if req.TargetScope != "account_global" {
					return errors.New("invalid_scope")
				}
			case "recover":
				if row.Health.MemberID == "" {
					return errors.New("credential_unavailable")
				}
				if req.GenerationID != row.Health.MemberID {
					return errors.New("stale_generation")
				}
			case "recover-group":
				// The model verifies this explicit group against stored membership under lock.
			default:
				return errors.New("invalid_action")
			}
			return model.MutateManagedAccount(c.Request.Context(), tx, row.Ref, row.Health, action, scope, req.QuotaGroup, req.ExpectedQuotaGroup, now)
		}
		return errors.New("stale_member")
	})
	if err != nil {
		accountPoolError(c, accountPoolErrorCode(err))
		return
	}
	params := map[string]interface{}{"channel_id": id, "profile_id": profileID, "pool_id": req.PoolID, "member_id": req.MemberID, "action": action, "operation": scope.Operation, "model": scope.Model, "target_scope": req.TargetScope, "result": "success"}
	if action == "recover" {
		params["generation_id"] = req.GenerationID
	}
	if action == "quota-group" || action == "recover-group" {
		params["quota_group"] = *req.QuotaGroup
		params["group_scope"] = "global"
	}
	recordManageAudit(c, "channel.account_pool."+action, params)
	common.ApiSuccess(c, gin.H{"applied": true, "action": action, "server_time": now, "refresh_required": true})
}

func accountPoolErrorCode(err error) string {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "channel_not_found"
	}
	switch err.Error() {
	case "not_pool", "invalid_scope", "configuration_unavailable", "model_not_in_channel", "wrong_profile", "wrong_pool", "pool_too_large", "credential_unavailable", "stale_generation", "stale_quota_group", "invalid_request", "invalid_action", "stale_member", "invalid upstream quota group":
		return err.Error()
	}
	return "state_unavailable"
}
func accountPoolError(c *gin.Context, code string) {
	status := http.StatusBadRequest
	if code == "state_unavailable" || code == "configuration_unavailable" {
		status = http.StatusServiceUnavailable
	}
	if strings.HasPrefix(code, "stale_") {
		status = http.StatusConflict
	}
	if code == "channel_not_found" {
		status = http.StatusNotFound
	}
	c.JSON(status, gin.H{"success": false, "message": code, "code": code})
}
