package router

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/platformbilling"
	"github.com/gin-gonic/gin"
	"io"
	"mime"
	"strings"
	"time"
)

func registerKnowledgeRoute(routes gin.IRoutes, a *platformbilling.Adapter) {
	routes.POST("/knowledge/route", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		fail := func(status int) {
			c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"code": "knowledge_route_denied"}})
		}
		auth, ok := singleKnowledgeHeader(c.Request.Header, "Authorization")
		ct, _, e := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if c.Request.URL.RawQuery != "" || e != nil || ct != "application/json" || c.Request.ContentLength > 1024 {
			fail(400)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 1025))
		if err != nil || len(raw) > 1024 {
			fail(400)
			return
		}
		var fields map[string]string
		if common.UnmarshalUnique(raw, &fields) != nil {
			fail(400)
			return
		}
		if !ok || !strings.HasPrefix(auth, "Bearer ") {
			fail(400)
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")
		if _, exists := c.Request.Header["Cookie"]; exists || c.GetHeader("Cookie") != "" {
			fail(400)
			return
		}
		if fields["purpose"] == "index_read" {
			if len(fields) != 5 || fields["account_id"] == "" || !platformbilling.ApprovedKnowledgePair(fields["model"], fields["wire_operation"]) || !platformbilling.ValidKnowledgeID(fields["policy_digest"]) {
				fail(400)
				return
			}
			for key := range c.Request.Header {
				lower := strings.ToLower(key)
				if strings.HasPrefix(lower, "x-unihub-") || lower == "idempotency-key" || lower == "x-idempotency-key" || lower == "x-call-id" || lower == "cookie" {
					fail(400)
					return
				}
			}
			if !platformbilling.OriginalKnowledgeActor(token) {
				fail(403)
				return
			}
			reply, err := a.ReadCurrentKnowledgeRoute(c.Request.Context(), token, fields["account_id"], fields["model"], fields["wire_operation"], fields["policy_digest"])
			if err != nil {
				fail(platformbilling.HTTPStatus(err))
				return
			}
			if reply.ExpiresAt <= time.Now().Unix() {
				fail(403)
				return
			}
			c.JSON(200, reply)
			return
		}
		if len(fields) != 2 || !platformbilling.ApprovedKnowledgePair(fields["model"], fields["wire_operation"]) {
			fail(400)
			return
		}
		actor, actorOK := singleKnowledgeHeader(c.Request.Header, "X-UniHub-Actor-Authorization")
		task, taskOK := singleKnowledgeHeader(c.Request.Header, "X-UniHub-Task-ID")
		if !actorOK || !taskOK || !strings.HasPrefix(actor, "Bearer ") {
			fail(400)
			return
		}
		reply, err := a.PaidKnowledgeRoute(c.Request.Context(), strings.TrimPrefix(actor, "Bearer "), token, task, fields["model"], fields["wire_operation"])
		if err != nil {
			fail(platformbilling.HTTPStatus(err))
			return
		}
		if reply.ExpiresAt <= time.Now().Unix() {
			fail(403)
			return
		}
		c.JSON(200, reply)
	})
}
