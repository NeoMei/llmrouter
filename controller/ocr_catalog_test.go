package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/modelcatalog"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOCRCatalogNativeWireAndOptions(t *testing.T) {
	oldDB := model.DB
	oldPrices := ratio_setting.ModelPrice2JSONString()
	oldMain, oldLog := common.MainDatabaseType(), common.LogDatabaseType()
	t.Cleanup(func() {
		model.DB = oldDB
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(oldPrices))
		common.SetDatabaseTypes(oldMain, oldLog)
	})
	db := setupClientModelRouteControllerTestDB(t)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"ocr-price":0.001}`))
	ch := model.Channel{Type: constant.ChannelTypePaddleOCR, Status: common.ChannelStatusEnabled, Models: "ocr", Name: "OCR", Key: "never-public"}
	require.NoError(t, db.Create(&ch).Error)
	payload := clientModelRoutePayload{PublicID: "public", Enabled: true, Metadata: `{"operations":["ocr.jobs.create","ocr.jobs.retrieve"],"async":true}`, Targets: []model.ClientModelTarget{{ChannelId: ch.Id, UpstreamModel: "ocr", BillingModel: "ocr-price", PreferredWires: `["paddleocr_jobs"]`, Enabled: true, Weight: 1}}}
	require.NoError(t, validateClientModelRoutePayload(db, payload))
	payload.Targets[0].PreferredWires = `["openai_chat"]`
	require.Error(t, validateClientModelRoutePayload(db, payload))
	ctx, w := clientModelRouteRequestContext(t, "GET", "/api/client_model_routes/options", nil)
	ListClientModelRouteOptions(ctx)
	assert.Contains(t, w.Body.String(), string(modelcatalog.WirePaddleOCR))
	assert.NotContains(t, w.Body.String(), "never-public")
}
