package accountpool

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
)

// AliFailure reads only documented structured inference errors. Ali's compatible
// insufficient_quota is TPM/TPS throttling, never evidence of empty funds.
// HTTP400 AllocationQuota can describe voice storage, so remains unclassified.
func AliFailure(status int, retryAfter string, raw []byte) (FailureInput, string) {
	input := FailureInput{StatusCode: status, RetryAfter: retryAfter}
	var envelope struct {
		Code  string `json:"code"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if len(raw) > 8192 || common.UnmarshalUnique(raw, &envelope) != nil {
		return input, ""
	}
	code := envelope.Code
	if envelope.Error != nil {
		if code != "" && code != envelope.Error.Code {
			return input, ""
		}
		code = envelope.Error.Code
	}
	switch {
	case status == http.StatusBadRequest && code == "Arrearage":
		input.Refusal = RefusalQuotaExhausted
		input.SharedQuota = true
	case status == http.StatusUnauthorized && (code == "InvalidApiKey" || code == "invalid_api_key"):
		input.Refusal = RefusalInvalidCredential
	case status == http.StatusTooManyRequests && (code == "Throttling.AllocationQuota" || code == "insufficient_quota"):
		// Status alone supplies ordinary operation/model cooldown.
	}
	return input, code
}
