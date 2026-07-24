package controller

import (
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/gateway"

	"github.com/gin-gonic/gin"
)

type byokRequest struct {
	ProfileId int    `json:"profile_id" binding:"required"`
	ApiKey    string `json:"api_key" binding:"required"`
	Label     string `json:"label"`
}

// AddUserProviderKey — POST /api/gateway/byok
// User adds their own provider API key (BYOK).
func AddUserProviderKey(c *gin.Context) {
	var req byokRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}

	aes := gateway.AESGCM()
	if aes == nil {
		common.ApiErrorMsg(c, "gateway encryption not configured (set GATEWAY_AES_KEY)")
		return
	}

	encrypted, err := aes.Encrypt([]byte(req.ApiKey))
	if err != nil {
		common.ApiError(c, err)
		return
	}

	key := model.UserProviderKey{
		UserId:       c.GetInt("id"),
		ProfileId:    req.ProfileId,
		EncryptedKey: encrypted,
		KeyLabel:     req.Label,
		Status:       "active",
	}
	if err := key.Insert(); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, key)
}

// DeleteUserProviderKey — DELETE /api/gateway/byok/:id
func DeleteUserProviderKey(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiErrorMsg(c, "invalid key id")
		return
	}
	found, err := model.DeleteUserProviderKeyById(id, c.GetInt("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !found {
		common.ApiErrorMsg(c, "key not found")
		return
	}
	common.ApiSuccess(c, nil)
}

// ListUserProviderKeys — GET /api/gateway/byok
func ListUserProviderKeys(c *gin.Context) {
	keys, err := model.GetUserProviderKeys(c.GetInt("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, keys)
}

type platformKeyRequest struct {
	ProfileId int    `json:"profile_id" binding:"required"`
	ApiKey    string `json:"api_key" binding:"required"`
}

// AddPlatformKey — POST /api/gateway/platform-key
// Admin adds a platform key to the shared pool.
func AddPlatformKey(c *gin.Context) {
	var req platformKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}

	aes := gateway.AESGCM()
	if aes == nil {
		common.ApiErrorMsg(c, "gateway encryption not configured (set GATEWAY_AES_KEY)")
		return
	}

	encrypted, err := aes.Encrypt([]byte(req.ApiKey))
	if err != nil {
		common.ApiError(c, err)
		return
	}

	key := model.ProviderKeyPool{
		ProfileId:    req.ProfileId,
		EncryptedKey: encrypted,
		Status:       "active",
	}
	if err := key.Insert(); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, key)
}

// ListPlatformKeys — GET /api/gateway/platform-key
// Admin views platform key pool status (optionally filtered by profile_id).
func ListPlatformKeys(c *gin.Context) {
	profileId, _ := strconv.Atoi(c.Query("profile_id"))
	keys, err := model.GetProviderKeyPools(profileId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, keys)
}
