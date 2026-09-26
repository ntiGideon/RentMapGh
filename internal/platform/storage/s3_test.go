package storage

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The worked examples from the AWS docs ("Signature Calculations for the
// Authorization Header"), so the signer is checked against a known answer.
const (
	awsAccessKey = "AKIAIOSFODNN7EXAMPLE"
	awsSecretKey = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY" //nolint:gosec // G101: AWS's public documentation key
	emptySHA256  = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

var awsExampleTime = time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)

func TestSignV4GetObject(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	signV4(req, emptySHA256, awsAccessKey, awsSecretKey, "us-east-1", awsExampleTime)
	assert.Equal(t, "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, "+
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, "+
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41", req.Header.Get("Authorization"))
}

func TestSignV4PutObject(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPut, "https://examplebucket.s3.amazonaws.com/test$file.text", strings.NewReader("Welcome to Amazon S3."))
	req.Header.Set("Date", "Fri, 24 May 2013 00:00:00 GMT")
	req.Header.Set("X-Amz-Storage-Class", "REDUCED_REDUNDANCY")
	signV4(req, "44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072", awsAccessKey, awsSecretKey, "us-east-1", awsExampleTime)
	assert.Contains(t, req.Header.Get("Authorization"), "SignedHeaders=date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class, "+
		"Signature=98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd")
}

func TestS3ConfigValidate(t *testing.T) {
	assert.Error(t, S3Config{}.Validate())
	assert.Error(t, S3Config{Endpoint: "localhost:8333", Bucket: "b", AccessKey: "a", SecretKey: "s"}.Validate())
	assert.NoError(t, S3Config{Endpoint: "http://localhost:8333", Bucket: "b", AccessKey: "a", SecretKey: "s"}.Validate())
}

// TestS3RoundTrip runs against a real bucket when TEST_S3_ENDPOINT is set
// (task test starts SeaweedFS with the dev stack).
func TestS3RoundTrip(t *testing.T) {
	endpoint := os.Getenv("TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_S3_ENDPOINT not set")
	}
	// One fixed bucket: every SeaweedFS bucket reserves several volumes, so a
	// fresh one per run would exhaust the dev server. The first run covers
	// the create path, later runs the "already there" path.
	bucket := "rentmap-test"
	s, err := NewS3(S3Config{Endpoint: endpoint, Region: "us-east-1", Bucket: bucket, PathStyle: true,
		AccessKey: envOr("TEST_S3_ACCESS_KEY_ID", "rentmap"), SecretKey: envOr("TEST_S3_SECRET_ACCESS_KEY", "rentmap-dev-secret")})
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, s.EnsureBucket(ctx))
	require.NoError(t, s.EnsureBucket(ctx), "idempotent")

	key := "media/" + uuid.NewString() + "/w320.jpg"
	require.NoError(t, s.Put(ctx, key, []byte("jpeg bytes")))
	got, err := s.Get(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, "jpeg bytes", string(got))
	require.NoError(t, s.Delete(ctx, key))
	_, err = s.Get(ctx, key)
	assert.ErrorIs(t, err, ErrNotFound)
	assert.NoError(t, s.Delete(ctx, key), "deleting twice is fine")

	bad, _ := NewS3(S3Config{Endpoint: endpoint, Region: "us-east-1", Bucket: bucket, PathStyle: true, AccessKey: "rentmap", SecretKey: "wrong"})
	assert.Error(t, bad.Put(ctx, key, []byte("x")), "bad signature is refused")
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
