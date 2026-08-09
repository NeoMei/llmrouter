package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/objectstore"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFilesLifecycleAndOwnership(t *testing.T) {
	router, manager := setupFileControllerTest(t, 1024)

	upload := uploadAPIFile(t, router, 41, "train.jsonl", "fine-tune", []byte("{\"prompt\":\"hello\"}\n"))
	require.Equal(t, http.StatusOK, upload.Code, upload.Body.String())
	var file dto.APIFileObject
	require.NoError(t, json.Unmarshal(upload.Body.Bytes(), &file))
	require.NotEmpty(t, file.ID)
	assert.Equal(t, "file", file.Object)
	assert.Equal(t, "train.jsonl", file.Filename)
	assert.Equal(t, "ready", file.Status)

	list := performFileRequest(router, 41, http.MethodGet, "/v1/files", nil, "")
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	var listed dto.APIFileList
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &listed))
	require.Len(t, listed.Data, 1)
	assert.Equal(t, file.ID, listed.Data[0].ID)

	metadata := performFileRequest(router, 41, http.MethodGet, "/v1/files/"+file.ID, nil, "")
	require.Equal(t, http.StatusOK, metadata.Code, metadata.Body.String())

	content := performFileRequest(router, 41, http.MethodGet, "/v1/files/"+file.ID+"/content", nil, "")
	require.Equal(t, http.StatusOK, content.Code, content.Body.String())
	assert.Equal(t, "{\"prompt\":\"hello\"}\n", content.Body.String())
	assert.Contains(t, content.Header().Get("Content-Disposition"), "train.jsonl")

	hidden := performFileRequest(router, 42, http.MethodGet, "/v1/files/"+file.ID, nil, "")
	assert.Equal(t, http.StatusNotFound, hidden.Code, hidden.Body.String())

	deleted := performFileRequest(router, 41, http.MethodDelete, "/v1/files/"+file.ID, nil, "")
	require.Equal(t, http.StatusOK, deleted.Code, deleted.Body.String())
	assert.Contains(t, deleted.Body.String(), `"deleted":true`)

	missing := performFileRequest(router, 41, http.MethodGet, "/v1/files/"+file.ID, nil, "")
	assert.Equal(t, http.StatusNotFound, missing.Code, missing.Body.String())
	_, _, err := manager.Store.Open(t.Context(), fmt.Sprintf("default/%d/%s", 41, file.ID))
	assert.Error(t, err)
}

func TestUploadFileRejectsOversizeAndMIMEConflict(t *testing.T) {
	router, _ := setupFileControllerTest(t, 8)

	oversize := uploadAPIFile(t, router, 7, "train.txt", "fine-tune", []byte("more-than-eight-bytes"))
	assert.Equal(t, http.StatusRequestEntityTooLarge, oversize.Code, oversize.Body.String())
	assert.Contains(t, oversize.Body.String(), "file_too_large")

	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 32)...)
	mismatch := uploadAPIFile(t, router, 7, "train.jsonl", "fine-tune", png)
	assert.Equal(t, http.StatusUnprocessableEntity, mismatch.Code, mismatch.Body.String())
	assert.Contains(t, mismatch.Body.String(), "invalid_file")
}

func TestListFilesValidatesLimit(t *testing.T) {
	router, _ := setupFileControllerTest(t, 1024)
	response := performFileRequest(router, 1, http.MethodGet, "/v1/files?limit=101", nil, "")
	assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "invalid_file")
}

func setupFileControllerTest(t *testing.T, maxBytes int64) (*gin.Engine, *service.APIFileManager) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	oldDB := model.DB
	oldLogDB := model.LOG_DB
	oldMainType := common.MainDatabaseType()
	oldLogType := common.LogDatabaseType()
	oldProvider := apiFileManagerProvider
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.APIFile{}))
	store, err := objectstore.NewLocal(t.TempDir())
	require.NoError(t, err)
	manager := &service.APIFileManager{Store: store, MaxBytes: maxBytes}
	apiFileManagerProvider = func() (*service.APIFileManager, error) { return manager, nil }
	t.Cleanup(func() {
		apiFileManagerProvider = oldProvider
		model.DB = oldDB
		model.LOG_DB = oldLogDB
		common.SetDatabaseTypes(oldMainType, oldLogType)
		sqlDB, openErr := db.DB()
		if openErr == nil {
			_ = sqlDB.Close()
		}
	})

	router := gin.New()
	router.Use(func(c *gin.Context) {
		if raw := c.GetHeader("X-Test-User-ID"); raw != "" {
			var userID int
			_, _ = fmt.Sscanf(raw, "%d", &userID)
			c.Set("id", userID)
		}
		c.Next()
	})
	router.GET("/v1/files", ListFiles)
	router.POST("/v1/files", UploadFile)
	router.GET("/v1/files/:id", GetFile)
	router.GET("/v1/files/:id/content", GetFileContent)
	router.DELETE("/v1/files/:id", DeleteFile)
	return router, manager
}

func uploadAPIFile(t *testing.T, router http.Handler, userID int, filename, purpose string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("purpose", purpose))
	part, err := writer.CreateFormFile("file", filename)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return performFileRequest(router, userID, http.MethodPost, "/v1/files", &body, writer.FormDataContentType())
}

func performFileRequest(router http.Handler, userID int, method, target string, body io.Reader, contentType string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, body)
	request.Header.Set("X-Test-User-ID", fmt.Sprintf("%d", userID))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
