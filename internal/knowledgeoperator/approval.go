package knowledgeoperator

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

var (
	errApproval = errors.New("invalid knowledge activation approval")
	revisionRE  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestRE    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Approval struct {
	Schema         int    `json:"schema"`
	Mode           string `json:"mode"`
	SourceRevision string `json:"source_revision"`
	InstanceID     string `json:"instance_id"`
	PrincipalID    int    `json:"principal_id"`
	Group          string `json:"group"`
	ConfigSHA256   string `json:"config_sha256"`
	PrestateSHA256 string `json:"prestate_sha256"`
}

func ParseApproval(raw []byte, expectedSHA256 string, apply bool) (Approval, error) {
	var approval Approval
	sum := sha256.Sum256(raw)
	if !digestRE.MatchString(expectedSHA256) || hex.EncodeToString(sum[:]) != expectedSHA256 {
		return approval, errApproval
	}
	var fields map[string]any
	if err := common.UnmarshalUnique(raw, &fields); err != nil || fields == nil {
		return approval, errApproval
	}
	allowed := map[string]bool{"schema": true, "mode": true, "source_revision": true, "instance_id": true, "principal_id": true, "group": true, "config_sha256": true, "prestate_sha256": true}
	if len(fields) != len(allowed) {
		return approval, errApproval
	}
	for key := range fields {
		if !allowed[key] {
			return approval, errApproval
		}
	}
	if err := common.UnmarshalUnique(raw, &approval); err != nil || approval.Schema != 1 || !revisionRE.MatchString(approval.SourceRevision) || strings.TrimSpace(approval.InstanceID) == "" || approval.PrincipalID < 1 || strings.TrimSpace(approval.Group) == "" || !digestRE.MatchString(approval.ConfigSHA256) || !digestRE.MatchString(approval.PrestateSHA256) {
		return Approval{}, errApproval
	}
	wantMode := "dry-run"
	if apply {
		wantMode = "apply"
	}
	if approval.Mode != wantMode {
		return Approval{}, errApproval
	}
	return approval, nil
}
