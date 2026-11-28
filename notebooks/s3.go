package notebooks

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Config configures any S3-compatible object store. Leave Endpoint empty to
// use AWS's regional endpoint; set it for compatible providers or local testing.
type S3Config struct {
	Bucket   string
	Region   string
	Endpoint string
}

// S3ObjectStore stores immutable file objects below a notebook-specific key
// prefix. Credentials come from the AWS SDK's standard role/environment chain.
type S3ObjectStore struct {
	client *s3.Client
	bucket string
}

// NewS3ObjectStore creates a client using the standard AWS credential chain.
func NewS3ObjectStore(ctx context.Context, cfg S3Config) (*S3ObjectStore, error) {
	if strings.TrimSpace(cfg.Bucket) == "" || strings.TrimSpace(cfg.Region) == "" {
		return nil, fmt.Errorf("S3 bucket and region are required")
	}
	loadOptions := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(cfg.Region)}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return nil, fmt.Errorf("load object storage configuration: %w", err)
	}
	clientOptions := []func(*s3.Options){}
	if cfg.Endpoint != "" {
		endpoint, err := url.Parse(cfg.Endpoint)
		if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.Fragment != "" {
			return nil, fmt.Errorf("invalid S3 endpoint")
		}
		clientOptions = append(clientOptions, func(options *s3.Options) {
			options.BaseEndpoint = aws.String(cfg.Endpoint)
			options.UsePathStyle = true
		})
	}
	return &S3ObjectStore{client: s3.NewFromConfig(awsCfg, clientOptions...), bucket: cfg.Bucket}, nil
}

func (s *S3ObjectStore) Put(ctx context.Context, notebookID, objectID, contentType string, size int64, body io.Reader) error {
	key, err := objectKey(notebookID, objectID)
	if err != nil {
		return err
	}
	if body == nil || size < 0 {
		return fmt.Errorf("%w: object body and non-negative size are required", ErrInvalidData)
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentLength: aws.Int64(size),
		ContentType:   aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("put S3 object: %w", err)
	}
	return nil
}

func (s *S3ObjectStore) Get(ctx context.Context, notebookID, objectID string) (io.ReadCloser, error) {
	key, err := objectKey(notebookID, objectID)
	if err != nil {
		return nil, err
	}
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, fmt.Errorf("get S3 object: %w", err)
	}
	return result.Body, nil
}

func (s *S3ObjectStore) Delete(ctx context.Context, notebookID, objectID string) error {
	key, err := objectKey(notebookID, objectID)
	if err != nil {
		return err
	}
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}); err != nil {
		return fmt.Errorf("delete S3 object: %w", err)
	}
	return nil
}

func objectKey(notebookID, objectID string) (string, error) {
	notebook, err := canonicalUUID(notebookID)
	if err != nil {
		return "", err
	}
	object, err := canonicalUUID(objectID)
	if err != nil {
		return "", err
	}
	return "notebooks/" + notebook + "/" + object, nil
}
