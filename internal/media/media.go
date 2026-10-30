// Package media stores uploaded images in MinIO (S3-compatible object storage).
package media

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

var (
	ErrTooLarge    = errors.New("image is too large")
	ErrUnsupported = errors.New("only JPEG, PNG, WebP and GIF images are allowed")
)

// Allowed types are sniffed from file content, never trusted from the client.
var allowed = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

type Store struct {
	client   *minio.Client
	bucket   string
	maxBytes int64
}

func New(ctx context.Context, endpoint, accessKey, secretKey, bucket string, useSSL bool, maxBytes int64) (*Store, error) {
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, err
	}
	exists, err := client.BucketExists(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("check bucket: %w", err)
	}
	if !exists {
		if err := client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("create bucket: %w", err)
		}
	}
	return &Store{client: client, bucket: bucket, maxBytes: maxBytes}, nil
}

// Upload validates, optimises and stores an image under prefix/<random>.<ext>, plus a
// thumbnail at ThumbKey(key). It returns the main object key. The bucket stays private;
// images are served through the app at /media/<key>.
func (s *Store) Upload(ctx context.Context, prefix string, r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, s.maxBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > s.maxBytes {
		return "", ErrTooLarge
	}
	if _, ok := allowed[http.DetectContentType(data)]; !ok {
		return "", ErrUnsupported
	}
	img, err := process(data)
	if err != nil {
		return "", err
	}
	key := prefix + "/" + randomName() + img.ext
	if err := s.put(ctx, key, img.main, img.contentType); err != nil {
		return "", err
	}
	if err := s.put(ctx, ThumbKey(key), img.thumb, img.contentType); err != nil {
		return "", err
	}
	return key, nil
}

func (s *Store) put(ctx context.Context, key string, data []byte, contentType string) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: contentType, CacheControl: "public, max-age=31536000, immutable"})
	if err != nil {
		return fmt.Errorf("upload object: %w", err)
	}
	return nil
}

// ThumbKey returns the thumbnail object key for an image key ("a/b.jpg" -> "a/b_thumb.jpg").
func ThumbKey(key string) string {
	ext := path.Ext(key)
	return strings.TrimSuffix(key, ext) + "_thumb" + ext
}

// MainKey reverses ThumbKey; ok is false when key is not a thumbnail key.
func MainKey(key string) (string, bool) {
	ext := path.Ext(key)
	base, found := strings.CutSuffix(strings.TrimSuffix(key, ext), "_thumb")
	return base + ext, found
}

// Object is an open image stream; the caller must Close it.
type Object struct {
	io.ReadCloser
	ContentType string
	Size        int64
	ETag        string
}

func (s *Store) Get(ctx context.Context, key string) (*Object, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	info, err := obj.Stat()
	if err != nil {
		obj.Close()
		return nil, err
	}
	return &Object{ReadCloser: obj, ContentType: info.ContentType, Size: info.Size, ETag: info.ETag}, nil
}

// Delete removes an image and its thumbnail.
func (s *Store) Delete(ctx context.Context, key string) error {
	_ = s.client.RemoveObject(ctx, s.bucket, ThumbKey(key), minio.RemoveObjectOptions{})
	return s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}

func (s *Store) Ping(ctx context.Context) error {
	_, err := s.client.BucketExists(ctx, s.bucket)
	return err
}

// IsUserError reports whether an upload failed validation (as opposed to a storage error).
func IsUserError(err error) bool {
	return errors.Is(err, ErrTooLarge) || errors.Is(err, ErrUnsupported) || errors.Is(err, ErrTooManyPixels)
}

func IsNotFound(err error) bool {
	return minio.ToErrorResponse(err).Code == "NoSuchKey"
}

func randomName() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
