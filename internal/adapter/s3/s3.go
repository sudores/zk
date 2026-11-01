// Package s3 synchronizes a notebook with an S3 bucket.
//
// Unlike the Git backend, which shells out to the `git` binary so that the
// user's own credential helper and configuration apply, this one links the AWS
// SDK. An object store has no merge: telling a note changed here from one
// changed somewhere else needs each object's ETag and the metadata zk writes
// beside it, which `aws s3 sync` does not expose. The SDK reads the same
// credential chain the AWS CLI does — the environment, `~/.aws/credentials`, a
// profile, SSO, an instance role — so the user's own setup still applies
// without the CLI having to be installed.
package s3

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go/logging"
	"github.com/zk-org/zk/internal/core"
)

const (
	// MetaMD5 carries the MD5 of the content zk uploaded. An ETag is the same
	// MD5 for an object stored in one part without encryption or with SSE-S3,
	// but is opaque under SSE-KMS, SSE-C or after a multipart upload, and this
	// is then the only way to ask whether two sides hold the same note.
	MetaMD5 = "zk-md5"
	// MetaMTime carries the file's modification time, so that a downloaded
	// note keeps the date it was written rather than the date it arrived: zk
	// shows a note's mtime as its modification date.
	MetaMTime = "zk-mtime"

	// deleteBatchSize is the most keys one DeleteObjects request takes.
	deleteBatchSize = 1000
)

// Object is one object in the bucket, as a listing describes it.
type Object struct {
	Key          string
	Size         int64
	ETag         string
	LastModified time.Time
}

// Reader is a downloaded object: its content, and the metadata zk left on it.
type Reader struct {
	Body     io.ReadCloser
	Metadata map[string]string
}

// ObjectStore is the slice of S3 a synchronization needs.
//
// It is an interface so that the whole synchronization can be exercised
// against an in-memory store, with no bucket, no credentials and no network.
type ObjectStore interface {
	// List returns every object under the prefix.
	List(ctx context.Context, prefix string) ([]Object, error)
	// Get downloads an object, metadata included.
	Get(ctx context.Context, key string) (*Reader, error)
	// Head returns an object's metadata without downloading it.
	Head(ctx context.Context, key string) (map[string]string, error)
	// Put uploads an object and returns its new ETag.
	Put(ctx context.Context, key string, body io.Reader, size int64, metadata map[string]string) (string, error)
	// Delete removes objects, which it may batch.
	Delete(ctx context.Context, keys []string) error
}

// Client is an ObjectStore backed by a real bucket.
type Client struct {
	api    *s3.Client
	bucket string
}

// NewClient connects to the bucket the configuration names.
//
// Nothing secret comes from the configuration: the credentials are whatever
// the AWS credential chain provides, which is why a notebook's own
// `config.toml` can be synchronized along with the notes.
func NewClient(ctx context.Context, config core.S3Config) (*Client, error) {
	loadOpts := []func(*awsconfig.LoadOptions) error{}
	if region := config.Region; !region.IsNull() {
		loadOpts = append(loadOpts, awsconfig.WithRegion(region.Unwrap()))
	}
	if profile := config.Profile; !profile.IsNull() {
		loadOpts = append(loadOpts, awsconfig.WithSharedConfigProfile(profile.Unwrap()))
	}

	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to read the AWS configuration: %w", err)
	}

	api := s3.NewFromConfig(awsConfig, func(o *s3.Options) {
		// zk says what is happening itself, and says nothing at all under
		// --quiet. The SDK's own logger would write past both, to standard
		// error, a debug line per object; what actually goes wrong comes back
		// as an error rather than as a log entry.
		o.Logger = logging.Nop{}

		if endpoint := config.EndpointURL; !endpoint.IsNull() {
			o.BaseEndpoint = aws.String(endpoint.Unwrap())
			// The SDK checksums every upload by default, which AWS understands
			// and several S3-compatible services do not: they reject the
			// request, or store the chunked framing as though it were the
			// note. The same goes for validating a checksum on the way back,
			// which a service that never sent one cannot satisfy. AWS keeps
			// the defaults, as a custom endpoint is where they go wrong.
			o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
			o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
		}
		o.UsePathStyle = config.PathStyle
	})

	return &Client{api: api, bucket: config.Bucket.Unwrap()}, nil
}

// List implements ObjectStore.
func (c *Client) List(ctx context.Context, prefix string) ([]Object, error) {
	var objects []Object

	pages := s3.NewListObjectsV2Paginator(c.api, &s3.ListObjectsV2Input{
		Bucket: aws.String(c.bucket),
		Prefix: aws.String(prefix),
	})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list s3://%s/%s: %w", c.bucket, prefix, err)
		}
		for _, object := range page.Contents {
			objects = append(objects, Object{
				Key:          aws.ToString(object.Key),
				Size:         aws.ToInt64(object.Size),
				ETag:         TrimETag(aws.ToString(object.ETag)),
				LastModified: aws.ToTime(object.LastModified),
			})
		}
	}
	return objects, nil
}

// Get implements ObjectStore.
func (c *Client) Get(ctx context.Context, key string) (*Reader, error) {
	out, err := c.api.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to download s3://%s/%s: %w", c.bucket, key, err)
	}
	return &Reader{Body: out.Body, Metadata: lowerKeys(out.Metadata)}, nil
}

// Head implements ObjectStore.
func (c *Client) Head(ctx context.Context, key string) (map[string]string, error) {
	out, err := c.api.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read the metadata of s3://%s/%s: %w", c.bucket, key, err)
	}
	return lowerKeys(out.Metadata), nil
}

// Put implements ObjectStore.
func (c *Client) Put(ctx context.Context, key string, body io.Reader, size int64, metadata map[string]string) (string, error) {
	out, err := c.api.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(c.bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentLength: aws.Int64(size),
		Metadata:      metadata,
	})
	if err != nil {
		return "", fmt.Errorf("failed to upload s3://%s/%s: %w", c.bucket, key, err)
	}
	return TrimETag(aws.ToString(out.ETag)), nil
}

// Delete implements ObjectStore.
func (c *Client) Delete(ctx context.Context, keys []string) error {
	for len(keys) > 0 {
		batch := keys
		if len(batch) > deleteBatchSize {
			batch = batch[:deleteBatchSize]
		}
		keys = keys[len(batch):]

		identifiers := make([]types.ObjectIdentifier, 0, len(batch))
		for _, key := range batch {
			identifiers = append(identifiers, types.ObjectIdentifier{Key: aws.String(key)})
		}

		out, err := c.api.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(c.bucket),
			Delete: &types.Delete{Objects: identifiers, Quiet: aws.Bool(true)},
		})
		if err != nil {
			return fmt.Errorf("failed to delete from s3://%s: %w", c.bucket, err)
		}
		// DeleteObjects reports a per-object failure in its response rather
		// than as an error, so a quiet request still has to be read.
		if len(out.Errors) > 0 {
			return fmt.Errorf("failed to delete s3://%s/%s: %s", c.bucket,
				aws.ToString(out.Errors[0].Key), aws.ToString(out.Errors[0].Message))
		}
	}
	return nil
}

// TrimETag strips the quotes S3 wraps an ETag in.
//
// It is done once, here at the boundary. Comparing a quoted ETag with an
// unquoted one would make every object look as though it had changed.
func TrimETag(etag string) string {
	return strings.Trim(etag, `"`)
}

// IsMD5 reports whether an ETag is a plain content MD5, and so says something
// about the content rather than only about the version.
func IsMD5(etag string) bool {
	if len(etag) != 32 {
		return false
	}
	for _, c := range etag {
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}

// lowerKeys normalizes metadata keys, which a service may hand back in any
// case it likes.
func lowerKeys(metadata map[string]string) map[string]string {
	out := make(map[string]string, len(metadata))
	for key, value := range metadata {
		out[strings.ToLower(key)] = value
	}
	return out
}
