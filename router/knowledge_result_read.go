package router

import (
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/gin-gonic/gin"
)

func singleKnowledgeHeader(header http.Header, name string) (string, bool) {
	var found []string
	for key, values := range header {
		if strings.EqualFold(key, name) {
			found = append(found, values...)
		}
	}
	returnValue := ""
	if len(found) == 1 {
		returnValue = found[0]
	}
	return returnValue, len(found) == 1
}

// The separately gated integration fixture returns the exact authenticated
// result; no caller-supplied actor, payer, model or digest is trusted.
func registerKnowledgeResultRead(routes gin.IRoutes, adapter *platformbilling.Adapter) {
	routes.POST("/knowledge/results/:callID", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		auth, authOK := singleKnowledgeHeader(c.Request.Header, "Authorization")
		actor, actorOK := singleKnowledgeHeader(c.Request.Header, "X-UniHub-Actor-Authorization")
		task, taskOK := singleKnowledgeHeader(c.Request.Header, "X-UniHub-Task-ID")
		if !authOK || !strings.HasPrefix(auth, "Bearer ") || !actorOK || !strings.HasPrefix(actor, "Bearer ") || !taskOK || c.Request.URL.RawQuery != "" || c.Request.ContentLength != 0 {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		if c.Request.Body != nil {
			body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1))
			if err != nil || len(body) != 0 {
				c.AbortWithStatus(http.StatusNotFound)
				return
			}
		}
		result, err := adapter.KnowledgeResult(c.Request.Context(), strings.TrimPrefix(actor, "Bearer "), strings.TrimPrefix(auth, "Bearer "), task, c.Param("callID"))
		if err != nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.Data(http.StatusOK, "application/json", result)
	})
}
