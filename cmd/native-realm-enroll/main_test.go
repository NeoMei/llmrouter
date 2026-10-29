package main

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestReviewedCohortExcludesUnknownAndDeletedKeys(t *testing.T) {
	owners := map[int]int{1: 2, 2: 3, 3: 6, 4: 4, 5: 3, 6: 5, 7: 4, 8: 10, 9: 4, 10: 6, 11: 4, 12: 10, 13: 11, 14: 12, 15: 11, 16: 11, 17: 11, 18: 1}
	m := model.NativeRealmManifest{Version: 1}
	for _, id := range []int{1, 2, 3, 4, 5, 6, 8, 9, 10, 11, 12, 13, 14} {
		u := model.NativeRealmEnrollment{UserID: id}
		for key, owner := range owners {
			if owner == id {
				u.Keys = append(u.Keys, model.NativeKeyEnrollment{ID: key, Deleted: key == 3 || key == 13})
			}
		}
		m.Users = append(m.Users, u)
	}
	raw, e := common.Marshal(m)
	require.NoError(t, e)
	assert.True(t, approvedCohort(raw))
	m.Users[0].Keys = append(m.Users[0].Keys, model.NativeKeyEnrollment{ID: 19})
	raw, e = common.Marshal(m)
	require.NoError(t, e)
	assert.False(t, approvedCohort(raw))
	m.Users[0].Keys = m.Users[0].Keys[:len(m.Users[0].Keys)-1]
	for i := range m.Users {
		for j := range m.Users[i].Keys {
			if m.Users[i].Keys[j].ID == 3 {
				m.Users[i].Keys[j].Deleted = false
			}
		}
	}
	raw, e = common.Marshal(m)
	require.NoError(t, e)
	assert.False(t, approvedCohort(raw))
}
