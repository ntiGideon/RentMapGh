package storage

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
)

// S3Config points at any S3-compatible bucket: SeaweedFS in dev, Cloudflare
// R2 or Garage in production.
type S3Config struct {
	Endpoint  string `env:"S3_ENDPOINT"` // e.g. http://localhost:8333 or https://<account>.r2.cloudflarestorage.com
	Region    string `env:"S3_REGION" envDefault:"us-east-1"` // R2: "auto"
	Bucket    string `env:"S3_BUCKET"`
	AccessKey string `env:"S3_ACCESS_KEY_ID"`
	SecretKey string `env:"S3_SECRET_ACCESS_KEY"`
	// PathStyle puts the bucket in the path (endpoint/bucket/key) instead of
	// the host name. SeaweedFS, Garage and R2 all accept it.
	PathStyle bool `env:"S3_PATH_STYLE" envDefault:"true"`
}

func (c S3Config) Validate() error {
	if c.Endpoint == "" || c.Bucket == "" || c.AccessKey == "" || c.SecretKey == "" {
		return errors.New("S3_ENDPOINT, S3_BUCKET, S3_ACCESS_KEY_ID and S3_SECRET_ACCESS_KEY are required when MEDIA_STORE=s3")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("S3_ENDPOINT must be an http(s) URL, got %q", c.Endpoint)
	}
	return nil
}

// S3 is a Store on an S3-compatible bucket. It signs requests itself (AWS
// Signature V4) — three verbs don't justify an SDK.
type S3 struct {
	cfg    S3Config
	base   *url.URL
	client *http.Client
	now    func() time.Time
}

// MaxObject caps what Get reads into memory.
const MaxObject = 256 << 20

func NewS3(cfg S3Config) (*S3, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	base, _ := url.Parse(strings.TrimSuffix(cfg.Endpoint, "/"))
	return &S3{cfg: cfg, base: base, client: &http.Client{Timeout: 60 * time.Second}, now: time.Now}, nil
}

func (s *S3) objectURL(key string) *url.URL {
	u := *s.base
	if s.cfg.PathStyle {
		u.Path = path.Join("/", s.cfg.Bucket, key)
	} else {
		u.Host = s.cfg.Bucket + "." + u.Host
		u.Path = path.Join("/", key)
	}
	if key == "" && s.cfg.PathStyle {
		u.Path = "/" + s.cfg.Bucket
	}
	return &u
}

func (s *S3) do(ctx context.Context, method, key string, body []byte, hdr map[string]string) (*http.Response, error) {
	if key != "" && !validKey.MatchString(key) {
		return nil, fmt.Errorf("storage: invalid key %q", key)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.objectURL(key).String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.ContentLength = int64(len(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	sum := sha256.Sum256(body)
	signV4(req, hex.EncodeToString(sum[:]), s.cfg.AccessKey, s.cfg.SecretKey, s.cfg.Region, s.now())
	return s.client.Do(req)
}

func (s *S3) Put(ctx context.Context, key string, data []byte) error {
	resp, err := s.do(ctx, http.MethodPut, key, data, map[string]string{"Content-Type": contentType(key)})
	if err != nil {
		return fmt.Errorf("storage: s3 put: %w", err)
	}
	defer drain(resp)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("storage: s3 put %s: %s", key, s3Error(resp))
	}
	return nil
}

func (s *S3) Get(ctx context.Context, key string) ([]byte, error) {
	resp, err := s.do(ctx, http.MethodGet, key, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("storage: s3 get: %w", err)
	}
	defer drain(resp)
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case resp.StatusCode/100 != 2:
		return nil, fmt.Errorf("storage: s3 get %s: %s", key, s3Error(resp))
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxObject+1))
	if err != nil {
		return nil, fmt.Errorf("storage: s3 read: %w", err)
	}
	if len(b) > MaxObject {
		return nil, fmt.Errorf("storage: s3 object %s is larger than %d bytes", key, MaxObject)
	}
	return b, nil
}

// Delete removes key; a missing key is not an error (S3 answers 204 anyway).
func (s *S3) Delete(ctx context.Context, key string) error {
	resp, err := s.do(ctx, http.MethodDelete, key, nil, nil)
	if err != nil {
		return fmt.Errorf("storage: s3 delete: %w", err)
	}
	defer drain(resp)
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("storage: s3 delete %s: %s", key, s3Error(resp))
	}
	return nil
}

// EnsureBucket creates the bucket if it doesn't exist (dev and first boot).
// It checks first, so production keys scoped to one bucket (no create
// permission) still boot.
func (s *S3) EnsureBucket(ctx context.Context) error {
	head, err := s.do(ctx, http.MethodHead, "", nil, nil)
	if err != nil {
		return fmt.Errorf("storage: s3 check bucket: %w", err)
	}
	drain(head)
	if head.StatusCode/100 == 2 {
		return nil
	}
	if head.StatusCode != http.StatusNotFound {
		return fmt.Errorf("storage: s3 check bucket %s: %s", s.cfg.Bucket, head.Status)
	}
	resp, err := s.do(ctx, http.MethodPut, "", nil, nil)
	if err != nil {
		return fmt.Errorf("storage: s3 create bucket: %w", err)
	}
	defer drain(resp)
	if resp.StatusCode/100 == 2 || resp.StatusCode == http.StatusConflict { // 409 BucketAlreadyOwnedByYou
		return nil
	}
	return fmt.Errorf("storage: s3 create bucket %s: %s", s.cfg.Bucket, s3Error(resp))
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}

func s3Error(resp *http.Response) string {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
	return strings.TrimSpace(resp.Status + " " + string(b))
}

func contentType(key string) string {
	switch path.Ext(key) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".png":
		return "image/png"
	}
	return "application/octet-stream"
}

// signV4 adds AWS Signature Version 4 headers to req. Every header already
// on req is signed, plus Host.
func signV4(req *http.Request, payloadHash, accessKey, secretKey, region string, t time.Time) {
	t = t.UTC()
	amzDate := t.Format("20060102T150405Z")
	day := t.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	headers := map[string]string{"host": req.URL.Host}
	for k, v := range req.Header {
		headers[strings.ToLower(k)] = strings.Join(strings.Fields(strings.Join(v, ",")), " ")
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, k := range names {
		canonHeaders.WriteString(k + ":" + headers[k] + "\n")
	}
	signed := strings.Join(names, ";")

	canonical := strings.Join([]string{
		req.Method,
		uriEncode(req.URL.Path, false),
		canonicalQuery(req.URL.Query()),
		canonHeaders.String(),
		signed,
		payloadHash,
	}, "\n")
	scope := day + "/" + region + "/s3/aws4_request"
	sum := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])

	key := hmacSHA256([]byte("AWS4"+secretKey), day)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, "s3")
	key = hmacSHA256(key, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(key, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, uriEncode(k, true)+"="+uriEncode(v, true))
		}
	}
	return strings.Join(parts, "&")
}

// uriEncode is AWS's URI encoding: everything except A–Z a–z 0–9 - _ . ~ is
// percent-encoded; "/" too unless it separates path segments.
func uriEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
