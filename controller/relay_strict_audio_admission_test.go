package controller

import (
	"bytes"
	"encoding/binary"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestStrictMultipartAudioRejectsBeforeReservationHTTP(t *testing.T) {
	for _, operation := range []struct{ path, operation string }{{"transcriptions", "audio.transcriptions.create"}, {"translations", "audio.translations.create"}} {
		t.Run(operation.path, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			db := setupTieredRelayBillingDB(t)
			require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ClientModelRoute{}, &model.ClientModelTarget{}, &model.Log{}))
			oldLogDB, oldLog, oldCache, oldRetry := model.LOG_DB, common.LogConsumeEnabled, common.MemoryCacheEnabled, common.RetryTimes
			model.LOG_DB, common.LogConsumeEnabled, common.MemoryCacheEnabled, common.RetryTimes = db, true, false, 1
			t.Setenv("LOG_SQL_DSN", "")
			require.NoError(t, model.InitLogDB())
			oldTranslator := common.TranslateMessage
			require.NoError(t, i18n.Init())
			t.Cleanup(func() {
				common.TranslateMessage = oldTranslator
				model.LOG_DB, common.LogConsumeEnabled, common.MemoryCacheEnabled, common.RetryTimes = oldLogDB, oldLog, oldCache, oldRetry
			})
			ratio_setting.InitRatioSettings()
			oldRatio := ratio_setting.ModelRatio2JSONString()
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"whisper-1":1}`))
			t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(oldRatio)) })
			createTieredRelayWalletFixtures(t, db, 910, 911, 100000, 100000, "strict-audio-token")
			service.InitHttpClient()
			service.InitTokenEncoders()
			var sends, financialUpdates atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sends.Add(1)
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"text":"fixture","usage":{"prompt_tokens":10,"total_tokens":10}}`)
			}))
			defer upstream.Close()
			require.NoError(t, db.Create(&model.Channel{Id: 921, Type: constant.ChannelTypeOpenAI, Name: "strict-audio", Key: "fixture", BaseURL: &upstream.URL, Models: "whisper-1", Group: "default", Status: common.ChannelStatusEnabled}).Error)
			require.NoError(t, db.Create(&model.Ability{ChannelId: 921, Group: "default", Model: "whisper-1", Enabled: true}).Error)
			require.NoError(t, db.Create(&model.ClientModelRoute{Id: 931, PublicId: "strict-audio-public", Enabled: true, Metadata: `{"input_modalities":["audio"],"operations":["` + operation.operation + `"],"media_limits":{"max_audio_seconds":1,"formats":["wav"]}}`}).Error)
			require.NoError(t, db.Create(&model.ClientModelTarget{Id: 932, RouteId: 931, ChannelId: 921, UpstreamModel: "whisper-1", BillingModel: "whisper-1", PreferredWires: `["openai_chat"]`, Weight: 1, Enabled: true}).Error)
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("strict_audio_no_hold", func(tx *gorm.DB) {
				if tx.Statement.Table == "users" || tx.Statement.Table == "tokens" {
					financialUpdates.Add(1)
				}
			}))
			t.Cleanup(func() { db.Callback().Update().Remove("strict_audio_no_hold") })
			engine := gin.New()
			engine.Use(func(c *gin.Context) {
				common.SetContextKey(c, constant.ContextKeyUserId, 910)
				common.SetContextKey(c, constant.ContextKeyTokenId, 911)
				common.SetContextKey(c, constant.ContextKeyTokenKey, "strict-audio-token")
				common.SetContextKey(c, constant.ContextKeyUserQuota, 100000)
				common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
				common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
				common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
				common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{BillingPreference: "wallet_only"})
				c.Next()
				common.CleanupBodyStorage(c)
			})
			path := "/v1/audio/" + operation.path
			engine.POST(path, middleware.Distribute(), func(c *gin.Context) { Relay(c, types.RelayFormatOpenAIAudio) })
			var body bytes.Buffer
			form := multipart.NewWriter(&body)
			require.NoError(t, form.WriteField("model", "strict-audio-public"))
			require.NoError(t, form.WriteField("response_format", "json"))
			file, err := form.CreateFormFile("file", "fixture.wav")
			require.NoError(t, err)
			wav := make([]byte, 44+16000)
			copy(wav, "RIFF")
			binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
			copy(wav[8:], "WAVEfmt ")
			binary.LittleEndian.PutUint32(wav[16:], 16)
			binary.LittleEndian.PutUint16(wav[20:], 1)
			binary.LittleEndian.PutUint16(wav[22:], 1)
			binary.LittleEndian.PutUint32(wav[24:], 8000)
			binary.LittleEndian.PutUint32(wav[28:], 16000)
			binary.LittleEndian.PutUint16(wav[32:], 2)
			binary.LittleEndian.PutUint16(wav[34:], 16)
			copy(wav[36:], "data")
			binary.LittleEndian.PutUint32(wav[40:], 16000)
			_, err = file.Write(wav)
			require.NoError(t, err)
			require.NoError(t, form.Close())
			request := httptest.NewRequest("POST", path, &body)
			request.Header.Set("Content-Type", form.FormDataContentType())
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
			assert.Contains(t, recorder.Body.String(), "verified media metadata")
			assert.Zero(t, sends.Load())
			assert.Zero(t, financialUpdates.Load(), "no reservation, settlement or refund may update money")
			wallet, token, used := tieredRelayQuotaState(t, db, 910, 911)
			assert.Equal(t, 100000, wallet)
			assert.Equal(t, 100000, token)
			assert.Zero(t, used)
			var logs int64
			require.NoError(t, db.Model(&model.Log{}).Where("type = ?", model.LogTypeConsume).Count(&logs).Error)
			assert.Zero(t, logs)
			require.Eventually(t, func() bool { return gopool.WorkerCount() == 0 }, 2*time.Second, time.Millisecond)
			assert.NotContains(t, strings.ToLower(recorder.Body.String()), "fixture token")
		})
	}
}
