package controller

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/service/relaymedia"
	"github.com/gin-gonic/gin"
)

var RelayMediaRuntime *relaymedia.Runtime // initialized before routes, closed before DB

type RelayMediaHandler struct{ Service *relaymedia.Service }

// Resource supports repeated GET, HEAD, and one bounded byte range. Multi-range
// requests are explicitly rejected. HEAD describes the whole object. Validation
// and hash verification precede any response bytes or object-specific headers.
func (h *RelayMediaHandler) Resource(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	if h == nil || h.Service == nil {
		c.Status(http.StatusNotFound)
		return
	}
	data, err := h.Service.ReadGranted(c.Request.Context(), c.Param("id"), c.Query("token"))
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Content-Type", "video/mp4")
	c.Header("Accept-Ranges", "bytes")
	start, end := int64(0), int64(len(data))-1
	status := http.StatusOK
	if raw := c.GetHeader("Range"); raw != "" && c.Request.Method == http.MethodGet {
		valid := strings.HasPrefix(raw, "bytes=")
		parts := strings.Split(strings.TrimPrefix(raw, "bytes="), "-")
		if !valid || len(parts) != 2 {
			valid = false
		} else if parts[0] == "" {
			n, e := strconv.ParseInt(parts[1], 10, 64)
			valid = e == nil && n > 0
			if valid {
				start = max(0, int64(len(data))-n)
			}
		} else {
			var e error
			start, e = strconv.ParseInt(parts[0], 10, 64)
			valid = e == nil && start >= 0 && start < int64(len(data))
			if parts[1] != "" {
				end, e = strconv.ParseInt(parts[1], 10, 64)
				valid = valid && e == nil && end >= start
				end = min(end, int64(len(data))-1)
			}
		}
		if !valid {
			c.Header("Content-Range", fmt.Sprintf("bytes */%d", len(data)))
			c.Status(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		status = http.StatusPartialContent
		c.Header("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
	}
	c.Header("Content-Length", strconv.FormatInt(end-start+1, 10))
	c.Status(status)
	if c.Request.Method == http.MethodGet {
		_, _ = c.Writer.Write(data[start : end+1])
	}
}
