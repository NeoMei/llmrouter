package objectstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type S3Config struct {
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	UsePathStyle    bool
}

type S3Store struct {
	client *s3.Client
	bucket string
}

func NewS3(config S3Config) (*S3Store, error) {
	endpoint, err := url.Parse(strings.TrimSpace(config.Endpoint))
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return nil, fmt.Errorf("invalid S3 endpoint")
	}
	if config.Region == "" || config.Bucket == "" || config.AccessKeyID == "" || config.SecretAccessKey == "" {
		return nil, fmt.Errorf("S3 region, bucket, and credentials are required")
	}
	awsConfig := aws.Config{
		Region:      config.Region,
		Credentials: credentials.NewStaticCredentialsProvider(config.AccessKeyID, config.SecretAccessKey, ""),
	}
	client := s3.NewFromConfig(awsConfig, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(endpoint.String())
		options.UsePathStyle = config.UsePathStyle
	})
	return &S3Store{client: client, bucket: config.Bucket}, nil
}

func (s *S3Store) Put(ctx context.Context, key string, body io.Reader, opts PutOptions) (ObjectInfo, error) {
	if _, err := validateS3Key(key); err != nil {
		return ObjectInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return ObjectInfo{}, err
	}
	staged, err := os.CreateTemp("", "llmrouter-object-upload-*")
	if err != nil {
		return ObjectInfo{}, err
	}
	stagedName := staged.Name()
	defer os.Remove(stagedName)
	defer staged.Close()

	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(staged, hash), body)
	if err != nil {
		return ObjectInfo{}, err
	}
	if opts.Size >= 0 && written != opts.Size {
		return ObjectInfo{}, fmt.Errorf("object size mismatch: declared %d, received %d", opts.Size, written)
	}
	checksum := hex.EncodeToString(hash.Sum(nil))
	if opts.ChecksumSHA256 != "" && !strings.EqualFold(opts.ChecksumSHA256, checksum) {
		return ObjectInfo{}, fmt.Errorf("object checksum mismatch")
	}
	if _, err := staged.Seek(0, io.SeekStart); err != nil {
		return ObjectInfo{}, err
	}
	input := &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), Body: staged,
		ContentType: aws.String(opts.ContentType),
		Metadata:    map[string]string{"sha256": checksum},
	}
	input.ContentLength = aws.Int64(written)
	output, err := s.client.PutObject(ctx, input)
	if err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{Key: key, ContentType: opts.ContentType, ETag: aws.ToString(output.ETag), Size: written}, nil
}

func (s *S3Store) Open(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	if _, err := validateS3Key(key); err != nil {
		return nil, ObjectInfo{}, err
	}
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	return output.Body, ObjectInfo{
		Key: key, ContentType: aws.ToString(output.ContentType), ETag: aws.ToString(output.ETag), Size: aws.ToInt64(output.ContentLength),
	}, nil
}

func (s *S3Store) Delete(ctx context.Context, key string) error {
	if _, err := validateS3Key(key); err != nil {
		return err
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	return err
}

func (s *S3Store) Health(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)})
	return err
}

func validateS3Key(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "..") || strings.Contains(key, "\\") {
		return "", fmt.Errorf("invalid object key")
	}
	return key, nil
}
