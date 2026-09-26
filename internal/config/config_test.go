package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	cfg, err := Load()
	require.NoError(t, err)
	assert.True(t, cfg.IsDev())
	assert.False(t, cfg.IsHTTPS())
	assert.Equal(t, ":8080", cfg.Addr)
}

func TestLoadRejectsUnsafeProduction(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("APP_ENV", "production")

	t.Setenv("BASE_URL", "http://rentmap.gh")
	_, err := Load()
	assert.ErrorContains(t, err, "https")

	t.Setenv("BASE_URL", "https://rentmap.gh")
	t.Setenv("STATIC_FROM_DISK", "true")
	_, err = Load()
	assert.ErrorContains(t, err, "STATIC_FROM_DISK")

	t.Setenv("STATIC_FROM_DISK", "false")
	_, err = Load()
	assert.ErrorContains(t, err, "AUTH_SECRET", "dev secret refused")

	t.Setenv("AUTH_SECRET", "0123456789abcdef0123456789abcdef")
	_, err = Load()
	assert.ErrorContains(t, err, "SMS_PROVIDER", "log provider refused")

	t.Setenv("SMS_PROVIDER", "africastalking")
	t.Setenv("AT_USERNAME", "rentmap")
	t.Setenv("AT_API_KEY", "k")
	t.Setenv("AT_SANDBOX", "false")
	_, err = Load()
	assert.ErrorContains(t, err, "LOCATION_SECRET")

	t.Setenv("LOCATION_SECRET", "abcdefghijabcdefghijabcdefghij12")
	_, err = Load()
	assert.ErrorContains(t, err, "DOCUMENT_KEY")

	t.Setenv("DOCUMENT_KEY", "c2hvcnQ=")
	_, err = Load()
	assert.ErrorContains(t, err, "32 bytes")

	t.Setenv("DOCUMENT_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	cfg, err := Load()
	require.NoError(t, err)
	k, err := cfg.DocumentKeyBytes()
	require.NoError(t, err)
	assert.Len(t, k, 32)
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	_, err := Load()
	assert.Error(t, err)
}

func TestLoadMediaStore(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "disk", cfg.MediaStore)

	t.Setenv("MEDIA_STORE", "ftp")
	_, err = Load()
	assert.ErrorContains(t, err, "MEDIA_STORE")

	t.Setenv("MEDIA_STORE", "s3")
	_, err = Load()
	assert.ErrorContains(t, err, "S3_ENDPOINT")

	t.Setenv("S3_ENDPOINT", "http://localhost:8333")
	t.Setenv("S3_BUCKET", "rentmap-media")
	t.Setenv("S3_ACCESS_KEY_ID", "rentmap")
	t.Setenv("S3_SECRET_ACCESS_KEY", "secret")
	cfg, err = Load()
	require.NoError(t, err)
	assert.True(t, cfg.S3.PathStyle)
	assert.Equal(t, "us-east-1", cfg.S3.Region)
}
