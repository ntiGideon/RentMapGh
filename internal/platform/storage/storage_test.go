package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiskRoundTripAndKeyValidation(t *testing.T) {
	ctx := context.Background()
	d := Disk{Root: t.TempDir()}
	require.NoError(t, d.Put(ctx, "evidence/abc/front.jpg", []byte("hello")))
	b, err := d.Get(ctx, "evidence/abc/front.jpg")
	require.NoError(t, err)
	assert.Equal(t, "hello", string(b))

	for _, bad := range []string{"../etc/passwd", "/abs", "a/../../b", "A/B", "a//b", "", `a\b`} {
		assert.Error(t, d.Put(ctx, bad, []byte("x")), bad)
	}

	require.NoError(t, d.Delete(ctx, "evidence/abc/front.jpg"))
	_, err = d.Get(ctx, "evidence/abc/front.jpg")
	assert.ErrorIs(t, err, ErrNotFound)
	assert.NoError(t, d.Delete(ctx, "evidence/abc/front.jpg"), "deleting twice is fine")
}

func TestSealedEncryptsAtRest(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	key := make([]byte, 32)
	s, err := NewSealed(Disk{Root: root}, key)
	require.NoError(t, err)

	secret := []byte("GHA-123456789-0 front of card")
	require.NoError(t, s.Put(ctx, "evidence/x/front.jpg", secret))

	raw, err := os.ReadFile(filepath.Join(root, "evidence", "x", "front.jpg"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "GHA-123456789-0", "plaintext never touches disk")

	got, err := s.Get(ctx, "evidence/x/front.jpg")
	require.NoError(t, err)
	assert.Equal(t, secret, got)

	// Ciphertext moved to another key fails authentication.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "evidence", "y"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "evidence", "y", "front.jpg"), raw, 0o600))
	_, err = s.Get(ctx, "evidence/y/front.jpg")
	assert.Error(t, err)

	// Wrong key fails too.
	other, _ := NewSealed(Disk{Root: root}, append(make([]byte, 31), 1))
	_, err = other.Get(ctx, "evidence/x/front.jpg")
	assert.Error(t, err)

	_, err = NewSealed(Disk{Root: root}, []byte("short"))
	assert.Error(t, err)
}
