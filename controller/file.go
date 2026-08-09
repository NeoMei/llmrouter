package controller

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const defaultTenantID = "default"

var apiFileManagerProvider = service.DefaultAPIFileManager

func ListFiles(c *gin.Context) {
	limit := 20
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeFileError(c, http.StatusBadRequest, "invalid_file", "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	files, err := model.ListAPIFiles(defaultTenantID, c.GetInt("id"), c.Query("after"), limit)
	if err != nil {
		writeFileError(c, http.StatusInternalServerError, "upstream_error", "failed to list files")
		return
	}
	objects := make([]dto.APIFileObject, 0, len(files))
	for index := range files {
		objects = append(objects, newAPIFileObject(&files[index]))
	}
	c.JSON(http.StatusOK, dto.APIFileList{Object: "list", Data: objects})
}

func UploadFile(c *gin.Context) {
	manager, err := apiFileManagerProvider()
	if err != nil {
		writeFileError(c, http.StatusInternalServerError, "upstream_error", "file storage is unavailable")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, manager.MaxBytes+(1<<20))
	reader, err := c.Request.MultipartReader()
	if err != nil {
		writeFileError(c, http.StatusBadRequest, "invalid_file", "multipart/form-data body is required")
		return
	}
	file, err := manager.Upload(c.Request.Context(), defaultTenantID, c.GetInt("id"), reader)
	if err != nil {
		writeAPIFileManagerError(c, err)
		return
	}
	c.JSON(http.StatusOK, newAPIFileObject(file))
}

func GetFile(c *gin.Context) {
	file, err := model.GetOwnedAPIFile(defaultTenantID, c.GetInt("id"), c.Param("id"))
	if err != nil {
		if model.IsAPIFileNotFound(err) {
			writeFileError(c, http.StatusNotFound, "resource_not_found", "file not found")
			return
		}
		writeFileError(c, http.StatusInternalServerError, "upstream_error", "failed to retrieve file")
		return
	}
	c.JSON(http.StatusOK, newAPIFileObject(file))
}

func GetFileContent(c *gin.Context) {
	manager, err := apiFileManagerProvider()
	if err != nil {
		writeFileError(c, http.StatusInternalServerError, "upstream_error", "file storage is unavailable")
		return
	}
	body, info, file, err := manager.Open(c.Request.Context(), defaultTenantID, c.GetInt("id"), c.Param("id"))
	if err != nil {
		writeAPIFileManagerError(c, err)
		return
	}
	defer body.Close()
	contentType := file.MediaType
	if contentType == "" {
		contentType = info.ContentType
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	c.Header("Content-Type", contentType)
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": file.Filename}))
	c.Status(http.StatusOK)
	if _, err := io.Copy(c.Writer, body); err != nil {
		_ = c.Error(err)
	}
}

func DeleteFile(c *gin.Context) {
	manager, err := apiFileManagerProvider()
	if err != nil {
		writeFileError(c, http.StatusInternalServerError, "upstream_error", "file storage is unavailable")
		return
	}
	deleted, err := manager.Delete(c.Request.Context(), defaultTenantID, c.GetInt("id"), c.Param("id"))
	if err != nil {
		writeAPIFileManagerError(c, err)
		return
	}
	c.JSON(http.StatusOK, dto.APIFileDeleteResponse{ID: c.Param("id"), Object: "file", Deleted: deleted})
}

func writeAPIFileManagerError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrAPIFileTooLarge):
		writeFileError(c, http.StatusRequestEntityTooLarge, "file_too_large", "file exceeds the configured size limit")
	case errors.Is(err, service.ErrAPIFileInvalid):
		writeFileError(c, http.StatusUnprocessableEntity, "invalid_file", err.Error())
	case errors.Is(err, service.ErrAPIFileNotFound):
		writeFileError(c, http.StatusNotFound, "resource_not_found", "file not found")
	default:
		writeFileError(c, http.StatusInternalServerError, "upstream_error", "file operation failed")
	}
}

func writeFileError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": types.OpenAIError{
		Message: message,
		Type:    "invalid_request_error",
		Code:    code,
	}})
}

func newAPIFileObject(file *model.APIFile) dto.APIFileObject {
	return dto.APIFileObject{
		ID: file.ID, Object: "file", Bytes: file.Bytes, CreatedAt: file.CreatedAt,
		Filename: file.Filename, Purpose: file.Purpose, Status: file.Status,
	}
}
