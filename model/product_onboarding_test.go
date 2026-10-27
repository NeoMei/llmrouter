package model

import (
	"context"
	"encoding/json"
	userkit "github.com/NeoMei/UniHub/packages/userkit-go"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/internal/platformaccount"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestFirstCentralLoginKeepsOriginalNewUserPolicyWithoutRegrantingOldMappings(t *testing.T) {
	old, redis, quota := DB, common.RedisEnabled, common.QuotaForNewUser
	db, e := gorm.Open(sqlite.Open(t.TempDir()+"/jit.db"), &gorm.Config{})
	require.NoError(t, e)
	require.NoError(t, db.AutoMigrate(&User{}, &UniHubIdentityMapping{}))
	DB, common.RedisEnabled, common.QuotaForNewUser = db, false, 150
	t.Cleanup(func() { DB, common.RedisEnabled, common.QuotaForNewUser = old, redis, quota })
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	id := platformaccount.Identity{Issuer: "https://hub.test", User: platformaccount.User{ID: "new-central-user", Name: "New user"}}
	u, e := ResolveUniHubProjection(id)
	require.NoError(t, e)
	require.Equal(t, 150, u.Quota, "central JIT must apply the original signup policy")
	require.NotEmpty(t, u.GetSetting().SidebarModules)
	require.True(t, u.ProductRegistrationPending)
	// Mapping reuse preserves consumed credit and explicitly completed policies.
	require.NoError(t, db.Model(&User{}).Where("id = ?", u.Id).Updates(map[string]any{"quota": 0, "product_registration_pending": false}).Error)
	u, e = ResolveUniHubProjection(id)
	require.NoError(t, e)
	require.Zero(t, u.Quota)
	require.False(t, u.ProductRegistrationPending)
}

func TestFirstCentralLoginRecoversLostSignupReplyAndCreatesOriginalDefaultKeyOnce(t *testing.T) {
	_, db, _ := productFundingFixture(t)
	require.NoError(t, db.AutoMigrate(&UniHubIdentityMapping{}, &ProductCreditAward{}))
	old, redis, quota, defaultKey := DB, common.RedisEnabled, common.QuotaForNewUser, constant.GenerateDefaultToken
	DB, common.RedisEnabled, common.QuotaForNewUser, constant.GenerateDefaultToken = db, false, 150, true
	t.Cleanup(func() {
		DB, common.RedisEnabled, common.QuotaForNewUser, constant.GenerateDefaultToken = old, redis, quota, defaultKey
	})
	awards := map[string]bool{}
	balance := 0
	fail := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/access") {
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/internal/v1/product-runtime/users/"), "/access")
			_ = json.NewEncoder(w).Encode(userkit.RuntimeAccess{Binding: userkit.RuntimeBinding{LocalUserID: id, ProductID: "llmrouter", InstanceID: "default", PersonalAccountID: "payer"}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/awards") {
			var in userkit.RuntimeAwardRequest
			_ = json.NewDecoder(r.Body).Decode(&in)
			if !awards[in.ReceiptID] {
				awards[in.ReceiptID] = true
				n, _ := strconv.Atoi(in.Amount)
				balance += n
			}
			if fail {
				fail = false
				w.WriteHeader(503)
				return
			}
			_ = json.NewEncoder(w).Encode(userkit.RuntimeAdjustment{ID: in.ReceiptID, AccountID: in.AccountID, Amount: in.Amount, Status: "APPLIED", ProductID: "llmrouter", InstanceID: "default"})
			return
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(server.Close)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_ENABLED", "true")
	t.Setenv("UNIHUB_ORIGIN", server.URL)
	t.Setenv("UNIHUB_PRODUCT_RUNTIME_TOKEN", strings.Repeat("t", 32))
	id := platformaccount.Identity{Issuer: "https://hub.test", User: platformaccount.User{ID: "jit-recover"}}
	u, e := ResolveUniHubProjection(id)
	require.NoError(t, e)
	require.Error(t, CompleteProductJITOnboarding(context.Background(), u.Id))
	require.Equal(t, 150, balance)
	for i := 0; i < 2; i++ {
		require.NoError(t, CompleteProductJITOnboarding(context.Background(), u.Id))
	}
	require.Len(t, awards, 1)
	require.Equal(t, 150, balance)
	var keys []Token
	require.NoError(t, db.Where("user_id = ?", u.Id).Find(&keys).Error)
	require.Len(t, keys, 1)
	require.True(t, keys[0].UnlimitedQuota)
	require.Equal(t, int64(-1), keys[0].ExpiredTime)
	require.NoError(t, db.First(&u, u.Id).Error)
	require.Zero(t, u.Quota)
	require.False(t, u.ProductRegistrationPending)
	require.False(t, u.ProductJITPending)
}
