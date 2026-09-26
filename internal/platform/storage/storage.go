// Package storage keeps files outside the database: private verification
// evidence (encrypted at rest) and user avatars.
//
// Disk is the v1 backend (a Docker volume in production). An S3-compatible
// backend (SeaweedFS/Garage/R2) joins it in Phase 2 with the media pipeline.
package storage

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// ErrNotFound is returned when a key has no object.
var ErrNotFound = errors.New("storage: not found")

// Store saves and loads whole objects by key. Keys are slash-separated,
// e.g. "evidence/0190.../front.jpg".
type Store interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
}

var validKey = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*(/[a-z0-9][a-z0-9._-]*)*$`)

// Disk stores objects as files under Root.
type Disk struct{ Root string }

func (d Disk) path(key string) (string, error) {
	if !validKey.MatchString(key) {
		return "", fmt.Errorf("storage: invalid key %q", key)
	}
	return filepath.Join(d.Root, filepath.FromSlash(key)), nil
}

// Put writes atomically (temp file + rename) with owner-only permissions.
func (d Disk) Put(_ context.Context, key string, data []byte) error {
	p, err := d.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("storage: mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".upload-*")
	if err != nil {
		return fmt.Errorf("storage: temp: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("storage: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("storage: close: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return fmt.Errorf("storage: chmod: %w", err)
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return fmt.Errorf("storage: rename: %w", err)
	}
	return nil
}

func (d Disk) Get(_ context.Context, key string) ([]byte, error) {
	p, err := d.path(key)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p) //nolint:gosec // G304: key is validated against a strict pattern
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}

// Delete removes key; a missing key is not an error.
func (d Disk) Delete(_ context.Context, key string) error {
	p, err := d.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("storage: delete: %w", err)
	}
	return nil
}

// Sealed encrypts objects with AES-256-GCM before handing them to the
// underlying store. A stolen disk or bucket is useless without the key.
type Sealed struct {
	store Store
	aead  cipher.AEAD
}

const sealedMagic = "RMS1" // format version, so the scheme can change later

// NewSealed wraps s with a 32-byte key.
func NewSealed(s Store, key []byte) (*Sealed, error) {
	if len(key) != 32 {
		return nil, errors.New("storage: encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealed{store: s, aead: aead}, nil
}

// Put encrypts data. The key is bound as additional data, so a ciphertext
// copied to another key fails to open.
func (s *Sealed) Put(ctx context.Context, key string, data []byte) error {
	out, err := s.Seal(key, data)
	if err != nil {
		return err
	}
	return s.store.Put(ctx, key, out)
}

func (s *Sealed) Get(ctx context.Context, key string) ([]byte, error) {
	b, err := s.store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return s.Open(key, b)
}

// Seal encrypts a small value (e.g. an ID number for a database column),
// bound to context so it can't be swapped between rows or columns.
func (s *Sealed) Seal(context string, plain []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("storage: nonce: %w", err)
	}
	out := make([]byte, 0, len(sealedMagic)+len(nonce)+len(plain)+s.aead.Overhead())
	out = append(out, sealedMagic...)
	out = append(out, nonce...)
	return s.aead.Seal(out, nonce, plain, []byte(context)), nil
}

// Open reverses Seal with the same context.
func (s *Sealed) Open(context string, b []byte) ([]byte, error) {
	ns := s.aead.NonceSize()
	if len(b) < len(sealedMagic)+ns || !bytes.Equal(b[:len(sealedMagic)], []byte(sealedMagic)) {
		return nil, errors.New("storage: not a sealed object")
	}
	nonce := b[len(sealedMagic) : len(sealedMagic)+ns]
	plain, err := s.aead.Open(nil, nonce, b[len(sealedMagic)+ns:], []byte(context))
	if err != nil {
		return nil, fmt.Errorf("storage: decrypt %s: %w", context, err)
	}
	return plain, nil
}

func (s *Sealed) Delete(ctx context.Context, key string) error { return s.store.Delete(ctx, key) }

// Memory is an in-memory Store for tests.
type Memory struct {
	mu sync.Mutex
	m  map[string][]byte
}

func NewMemory() *Memory { return &Memory{m: map[string][]byte{}} }

func (m *Memory) Put(_ context.Context, key string, data []byte) error {
	if !validKey.MatchString(key) {
		return fmt.Errorf("storage: invalid key %q", key)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.m[key] = append([]byte(nil), data...)
	return nil
}

func (m *Memory) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.m[key]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), b...), nil
}

func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.m, key)
	return nil
}

// Len reports how many objects are stored (tests).
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.m)
}
