package main

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApprovedQuotaCohortPreservesAllOriginalKeysAndExcludesDeleted(t *testing.T) {
	cutover := strings.Repeat("a", 64)
	manifest := model.NativeTokenQuotaManifest{Version: 1, CutoverEvidence: cutover}
	owners := map[int]int{1: 2, 2: 3, 3: 6, 4: 4, 5: 3, 6: 5, 7: 4, 8: 10, 9: 4, 10: 6, 11: 4, 12: 10, 13: 11, 14: 12, 15: 11, 16: 11, 17: 11, 18: 1}
	for id := 1; id <= 18; id++ {
		manifest.Inventory = append(manifest.Inventory, model.NativeTokenQuotaInventory{TokenID: id, UserID: owners[id], KeySHA256: strings.Repeat("b", 64), Deleted: id == 3 || id == 13})
		if id != 3 && id != 13 {
			manifest.Tokens = append(manifest.Tokens, model.NativeTokenQuotaImport{TokenID: id, UserID: owners[id], KeySHA256: strings.Repeat("b", 64), RemainQuota: 100})
		}
	}
	raw, err := common.Marshal(manifest)
	require.NoError(t, err)
	assert.True(t, approvedCohort(raw, cutover))
	assert.False(t, approvedCohort(raw, strings.Repeat("c", 64)))
	manifest.Tokens[0].TokenID = 3
	raw, err = common.Marshal(manifest)
	require.NoError(t, err)
	assert.False(t, approvedCohort(raw, cutover), "soft-deleted key must never be imported into the live quota cohort")
	manifest.Tokens = nil
	manifest.Inventory = nil
	raw, err = common.Marshal(manifest)
	require.NoError(t, err)
	assert.False(t, approvedCohort(raw, cutover), "new-key fixture activation cannot attest the original production cohort")
}
