package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"server/globals"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsConfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type R2CompatibleStore struct {
	cfg           ObjectStoreConfig
	client        *s3.Client
	presignClient *s3.PresignClient
}

var (
	objectStoreMu    sync.Mutex
	objectStoreCache = map[string]*R2CompatibleStore{}
)

func NewR2CompatibleStore(cfg ObjectStoreConfig) (*R2CompatibleStore, error) {
	cacheKey := strings.Join([]string{
		cfg.Provider,
		cfg.Endpoint,
		cfg.Region,
		cfg.Bucket,
		cfg.AccessKeyID,
		cfg.SecretAccessKey,
		cfg.CDNUrl,
		cfg.PublicURL,
		cfg.PathPrefix,
		fmt.Sprintf("%t", cfg.ForcePathStyle),
		fmt.Sprintf("%t", cfg.UsePresignedURL),
		fmt.Sprintf("%d", cfg.PresignTTLSeconds),
	}, "|")

	objectStoreMu.Lock()
	existing := objectStoreCache[cacheKey]
	objectStoreMu.Unlock()
	if existing != nil && existing.client != nil {
		return existing, nil
	}

	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		if cfg.Provider == "r2" {
			region = "auto"
		} else {
			region = "us-east-1"
		}
	}

	loadOpts := []func(*awsConfig.LoadOptions) error{
		awsConfig.WithRegion(region),
	}
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		loadOpts = append(loadOpts, awsConfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		))
	}

	awsCfg, err := awsConfig.LoadDefaultConfig(context.Background(), loadOpts...)
	if err != nil {
		return nil, err
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(strings.TrimSuffix(cfg.Endpoint, "/"))
			o.UsePathStyle = cfg.ForcePathStyle || cfg.Provider == "r2"
		}
	})

	store := &R2CompatibleStore{
		cfg:           cfg,
		client:        client,
		presignClient: s3.NewPresignClient(client),
	}

	objectStoreMu.Lock()
	objectStoreCache[cacheKey] = store
	objectStoreMu.Unlock()

	return store, nil
}

func (s *R2CompatibleStore) Put(ctx context.Context, req *PutObjectRequest) (*PutObjectResult, error) {
	if s == nil || s.client == nil {
		return nil, fmt.Errorf("object store not initialized")
	}
	if req == nil {
		return nil, fmt.Errorf("put request is nil")
	}
	if strings.TrimSpace(req.Key) == "" {
		return nil, fmt.Errorf("object key is required")
	}
	if req.Body == nil {
		return nil, fmt.Errorf("object body is required")
	}

	input := &s3.PutObjectInput{
		Bucket:       aws.String(s.cfg.Bucket),
		Key:          aws.String(req.Key),
		Body:         req.Body,
		ContentType:  aws.String(req.ContentType),
		CacheControl: aws.String(req.CacheControl),
		Metadata:     req.Metadata,
	}
	if req.Size > 0 {
		input.ContentLength = aws.Int64(req.Size)
	}

	var (
		outETag string
		err     error
	)
	if req.Size >= NormalizeMultipartThresholdBytes(s.cfg.MultipartThresholdBytes) {
		outETag, err = s.putMultipart(ctx, input, req.Size)
	} else {
		out, putErr := s.client.PutObject(ctx, input)
		if putErr != nil {
			return nil, fmt.Errorf("put object failed: %w", putErr)
		}
		outETag = strings.Trim(strings.TrimSpace(aws.ToString(out.ETag)), `"`)
	}
	if err != nil {
		return nil, err
	}

	publicURL := BuildPublicObjectURL(s.cfg, req.Key)
	if s.cfg.Provider == "r2" && s.cfg.CDNUrl == "" && s.cfg.PublicURL == "" && strings.Contains(s.cfg.Endpoint, ".r2.cloudflarestorage.com") {
		globals.Error("R2 endpoint configured without cdn_url/public_url; generated URL may be inaccessible")
	}

	return &PutObjectResult{
		Provider:  s.cfg.Provider,
		Bucket:    s.cfg.Bucket,
		Key:       req.Key,
		ETag:      outETag,
		PublicURL: publicURL,
	}, nil
}

func (s *R2CompatibleStore) putMultipart(ctx context.Context, input *s3.PutObjectInput, size int64) (string, error) {
	partSizeBytes := NormalizeMultipartPartSizeBytes(s.cfg.MultipartPartSizeBytes)

	createOut, err := s.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket:       input.Bucket,
		Key:          input.Key,
		ContentType:  input.ContentType,
		CacheControl: input.CacheControl,
		Metadata:     input.Metadata,
	})
	if err != nil {
		return "", fmt.Errorf("create multipart upload failed: %w", err)
	}

	uploadID := aws.ToString(createOut.UploadId)
	completedParts := make([]types.CompletedPart, 0, maxMultipartParts(size, partSizeBytes))
	abort := func() {
		_, _ = s.client.AbortMultipartUpload(context.Background(), &s3.AbortMultipartUploadInput{
			Bucket:   input.Bucket,
			Key:      input.Key,
			UploadId: aws.String(uploadID),
		})
	}

	partNumber := int32(1)
	remaining := size
	for remaining > 0 {
		partSize := partSizeBytes
		if remaining < partSize {
			partSize = remaining
		}
		buffer := make([]byte, int(partSize))
		if _, err := io.ReadFull(input.Body, buffer); err != nil {
			abort()
			return "", fmt.Errorf("read multipart part failed: %w", err)
		}
		partOut, err := s.client.UploadPart(ctx, &s3.UploadPartInput{
			Bucket:        input.Bucket,
			Key:           input.Key,
			UploadId:      aws.String(uploadID),
			PartNumber:    aws.Int32(partNumber),
			Body:          bytes.NewReader(buffer),
			ContentLength: aws.Int64(partSize),
		})
		if err != nil {
			abort()
			return "", fmt.Errorf("upload multipart part failed: %w", err)
		}
		completedParts = append(completedParts, types.CompletedPart{
			ETag:       partOut.ETag,
			PartNumber: aws.Int32(partNumber),
		})
		remaining -= partSize
		partNumber++
	}

	completeOut, err := s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   input.Bucket,
		Key:      input.Key,
		UploadId: aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: completedParts,
		},
	})
	if err != nil {
		abort()
		return "", fmt.Errorf("complete multipart upload failed: %w", err)
	}

	return strings.Trim(strings.TrimSpace(aws.ToString(completeOut.ETag)), `"`), nil
}

func maxMultipartParts(size int64, partSize int64) int {
	if partSize <= 0 {
		partSize = NormalizeMultipartPartSizeBytes(0)
	}
	parts := int(size / partSize)
	if size%partSize != 0 {
		parts++
	}
	if parts < 1 {
		return 1
	}
	return parts
}

func (s *R2CompatibleStore) Delete(ctx context.Context, key string) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("object store not initialized")
	}
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("object key is required")
	}

	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("delete object failed: %w", err)
	}
	return nil
}

func (s *R2CompatibleStore) Get(ctx context.Context, key string) (io.ReadCloser, string, error) {
	if s == nil || s.client == nil {
		return nil, "", fmt.Errorf("object store not initialized")
	}
	if strings.TrimSpace(key) == "" {
		return nil, "", fmt.Errorf("object key is required")
	}

	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, "", fmt.Errorf("get object failed: %w", err)
	}

	return out.Body, strings.TrimSpace(aws.ToString(out.ContentType)), nil
}

func (s *R2CompatibleStore) PublicURL(key string) string {
	return BuildPublicObjectURL(s.cfg, key)
}

func (s *R2CompatibleStore) DownloadURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return s.DownloadURLWithDisposition(ctx, key, ttl, "")
}

func (s *R2CompatibleStore) DownloadURLWithDisposition(ctx context.Context, key string, ttl time.Duration, contentDisposition string) (string, error) {
	if s == nil || s.client == nil {
		return "", fmt.Errorf("object store not initialized")
	}
	if strings.TrimSpace(key) == "" {
		return "", fmt.Errorf("object key is required")
	}
	trimmedDisposition := strings.TrimSpace(contentDisposition)
	if !s.cfg.UsePresignedURL && trimmedDisposition == "" {
		return s.PublicURL(key), nil
	}
	if ttl <= 0 {
		if s.cfg.PresignTTLSeconds > 0 {
			ttl = time.Duration(s.cfg.PresignTTLSeconds) * time.Second
		} else {
			ttl = time.Hour
		}
	}
	if s.presignClient == nil {
		s.presignClient = s3.NewPresignClient(s.client)
	}
	input := &s3.GetObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(key),
	}
	if trimmedDisposition != "" {
		input.ResponseContentDisposition = aws.String(trimmedDisposition)
	}
	result, err := s.presignClient.PresignGetObject(ctx, input, func(opts *s3.PresignOptions) {
		opts.Expires = ttl
	})
	if err != nil {
		return "", fmt.Errorf("presign get object failed: %w", err)
	}
	return result.URL, nil
}

func (s *R2CompatibleStore) Provider() string {
	return s.cfg.Provider
}

func (s *R2CompatibleStore) Bucket() string {
	return s.cfg.Bucket
}

// RawClient exposes the underlying S3 client for callers (CLIs, sync tools)
// that need HEAD/list operations not covered by ObjectStore.
func (s *R2CompatibleStore) RawClient() *s3.Client {
	return s.client
}
