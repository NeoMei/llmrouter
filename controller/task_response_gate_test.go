package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestPendingResponseGateRetryDiscardsFailedAttempt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/suno/submit/LYRICS", nil)
	writer := c.Writer
	writer.Header().Set("X-Original", "keep")

	gate := newPendingResponseGate(writer)
	c.Writer = gate

	// A failed attempt emits only an error status/header before the retry.
	c.Writer.Header().Set("X-Failed-Attempt", "stale")
	c.Writer.WriteHeader(http.StatusInternalServerError)
	require.True(t, c.Writer.Written())

	gate.reset()
	require.False(t, gate.Written())
	require.Equal(t, http.StatusOK, gate.Status())
	require.Equal(t, "keep", gate.Header().Get("X-Original"))
	require.Empty(t, gate.Header().Get("X-Failed-Attempt"))

	c.JSON(http.StatusOK, gin.H{"ok": true})
	require.Equal(t, http.StatusOK, gate.Status())
	require.False(t, gate.ResponseWriter.Written(), "the buffered response must stay private until commit")
	gate.commit()
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "keep", recorder.Header().Get("X-Original"))
	require.Empty(t, recorder.Header().Get("X-Failed-Attempt"))
	require.JSONEq(t, `{"ok":true}`, recorder.Body.String())
}

func TestPendingResponseGateCommitPreservesEmptyWrittenHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/suno/submit/LYRICS", nil)
	gate := newPendingResponseGate(c.Writer)

	gate.WriteHeader(http.StatusNoContent)
	require.True(t, gate.Written())
	gate.commit()
	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.Empty(t, recorder.Body.String())
}
