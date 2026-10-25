package helper

import (
	"encoding/base64"
	"fmt"
	"io"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
)

// ValidateNativeAudioAdmission certifies only bounded PCM WAV bytes; unsupported
// codecs remain unavailable rather than substituting request token estimates.
func ValidateNativeAudioAdmission(c *gin.Context, info *relaycommon.RelayInfo) error {
	if info.RelayMode != relayconstant.RelayModeAudioTranscription && info.RelayMode != relayconstant.RelayModeAudioTranslation {
		return nil
	}
	form, err := common.ParseMultipartFormReusable(c)
	if err != nil {
		return err
	}
	files := form.File["file"]
	if len(files) != 1 || files[0].Size <= 0 || files[0].Size > MaxInlineMediaBytes {
		return fmt.Errorf("native transcription requires one bounded WAV file")
	}
	f, err := files[0].Open()
	if err != nil {
		return err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, MaxInlineMediaBytes+1))
	if err != nil || len(raw) > MaxInlineMediaBytes {
		return fmt.Errorf("audio exceeds measurement bound")
	}
	fact, err := DecodeMediaFact("audio", base64.StdEncoding.EncodeToString(raw))
	if err != nil || fact.Format != "wav" || fact.Seconds <= 0 || fact.Seconds > 86400 {
		return fmt.Errorf("native transcription requires measured PCM WAV duration")
	}
	info.ActualAudioSeconds = &fact.Seconds
	return nil
}
