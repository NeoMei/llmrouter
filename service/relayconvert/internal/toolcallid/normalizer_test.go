package toolcallid

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizerPreservesValidIDs(t *testing.T) {
	n := New("call")
	for _, id := range []string{"call_1", "tool-use_ABC-123"} {
		assert.Equal(t, id, n.Normalize(id))
		got, ok := n.Lookup(id)
		require.True(t, ok)
		assert.Equal(t, id, got)
	}
	valid64 := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-"
	require.Len(t, valid64, 64)
	assert.Equal(t, valid64, n.Normalize(valid64))
}

func TestNormalizerIsStableCollisionFreeAndRequestLocal(t *testing.T) {
	a := New("call")
	first := a.Normalize("bad/id")
	assert.Equal(t, first, a.Normalize("bad/id"))
	second := a.Normalize("bad id")
	assert.NotEqual(t, first, second)
	assert.NotEqual(t, "call", a.Normalize(" call "))
	assert.NotEqual(t, a.Normalize(" call "), a.Normalize("call"))
	assert.Regexp(t, regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`), first)

	b := New("call")
	assert.Equal(t, first, b.Normalize("bad/id"))
	_, ok := b.Lookup("bad id")
	assert.False(t, ok, "separate normalizers must not share forward state")
}

func TestNormalizerEmptyIDsUseDeterministicOrdinals(t *testing.T) {
	a := New("call")
	a1 := a.Normalize("")
	a2 := a.Normalize("")
	require.NotEqual(t, a1, a2)
	firstHash := sha256.Sum256([]byte("1"))
	secondHash := sha256.Sum256([]byte("2"))
	assert.Equal(t, "call_"+hex.EncodeToString(firstHash[:])[:24], a1)
	assert.Equal(t, "call_"+hex.EncodeToString(secondHash[:])[:24], a2)
	assert.False(t, func() bool { _, ok := a.Lookup(""); return ok }())

	b := New("call")
	assert.Equal(t, a1, b.Normalize(""))
	assert.Equal(t, a2, b.Normalize(""))
}

func TestNormalizerSeparatesCallRegistrationFromResultLookup(t *testing.T) {
	n := New("call")
	registered, err := n.RegisterCall("call_1")
	require.NoError(t, err)
	assert.Equal(t, "call_1", registered)
	resolved, ok := n.LookupResult("call_1")
	require.True(t, ok)
	assert.Equal(t, registered, resolved)

	_, err = n.RegisterCall("call_1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate tool call id")

	empty1, err := n.RegisterCall("")
	require.NoError(t, err)
	empty2, err := n.RegisterCall("")
	require.NoError(t, err)
	assert.NotEqual(t, empty1, empty2)
	_, ok = n.LookupResult("")
	assert.False(t, ok, "empty result IDs cannot safely identify an ordinal call")
}

func TestNormalizerResolvesCandidateCollisionAndTracksReverse(t *testing.T) {
	n := New("call")
	sum := sha256.Sum256([]byte("bad/id"))
	base := "call_" + hex.EncodeToString(sum[:])[:24]
	assert.Equal(t, base, n.Normalize(base))

	got := n.Normalize("bad/id")
	assert.Equal(t, base+"_2", got)
	assert.LessOrEqual(t, len(got), 64)
	source, ok := n.Source(got)
	require.True(t, ok)
	assert.Equal(t, "bad/id", source)
}
