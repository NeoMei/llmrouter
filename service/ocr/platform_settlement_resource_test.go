package ocr

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	base "github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlatformOCRExistingOutputGrantRequiresSettlement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	svc, _ := serviceFixture(t, server)
	ctx := context.Background()
	job := model.OCRJob{ID: "job", Funding: "platform", PlatformCallID: "call", CreatedAt: time.Now().Unix()}
	require.NoError(t, svc.db.Create(&job).Error)
	output, err := svc.storeResource(ctx, job.ID, "image", "image/png", []byte("output"), time.Now().Add(time.Hour).Unix())
	require.NoError(t, err)
	input, err := svc.storeResource(ctx, job.ID, "input", "image/png", []byte("input"), time.Now().Add(time.Hour).Unix())
	require.NoError(t, err)
	require.NoError(t, svc.db.Create(&output).Error)
	require.NoError(t, svc.db.Create(&input).Error)
	outputURL, err := svc.resourceURL(ctx, output)
	require.NoError(t, err)
	inputURL, err := svc.resourceURL(ctx, input)
	require.NoError(t, err)
	out, err := url.Parse(outputURL)
	require.NoError(t, err)
	in, err := url.Parse(inputURL)
	require.NoError(t, err)
	old := base.PlatformTaskSettlementCheck
	t.Cleanup(func() { base.PlatformTaskSettlementCheck = old })
	base.PlatformTaskSettlementCheck = nil
	_, _, err = svc.OpenGrantedResource(ctx, job.ID, output.ID, out.Query().Get("token"))
	require.Error(t, err)
	reader, _, err := svc.OpenGrantedResource(ctx, job.ID, input.ID, in.Query().Get("token"))
	require.NoError(t, err)
	raw, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	assert.Equal(t, "input", string(raw))
	base.PlatformTaskSettlementCheck = func(_ context.Context, call string) error { assert.Equal(t, "call", call); return nil }
	reader, _, err = svc.OpenGrantedResource(ctx, job.ID, output.ID, out.Query().Get("token"))
	require.NoError(t, err)
	raw, err = io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	assert.Equal(t, "output", string(raw))
}
