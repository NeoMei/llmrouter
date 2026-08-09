package logger

import (
	"io"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

func TestConcurrentLoggingDoesNotRace(t *testing.T) {
	common.LogWriterMu.Lock()
	previousWriter := gin.DefaultWriter
	previousErrorWriter := gin.DefaultErrorWriter
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultWriter = previousWriter
		gin.DefaultErrorWriter = previousErrorWriter
		common.LogWriterMu.Unlock()
	})

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				LogInfo(nil, "race probe")
			}
		}()
	}
	wg.Wait()
}
