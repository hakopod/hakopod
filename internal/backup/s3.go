package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const PartSize = 8 << 20
const DefaultMaxBytes int64 = 8 << 30

// S3 caps a list page and a batched delete at 1000 keys each, so one page is
// one round trip for both operations.
const PrefixPageSize = 1000

// One DeletePrefix call does at most ten list-and-delete round trips, so a
// large tree cannot hold a retention worker for an unbounded time. The caller
// resumes on the next pass.
const MaxPrefixDeletePerCall = 10 * PrefixPageSize

type ObjectStore interface {
	Put(context.Context, string, io.Reader, int64) (int64, string, error)
	Get(context.Context, string) (io.ReadCloser, int64, error)
	Delete(context.Context, string) error
	// ListPrefix returns one bounded page of keys under prefix plus the
	// continuation token for the next page, or an empty token at the end.
	ListPrefix(ctx context.Context, prefix string, token string) (keys []string, next string, err error)
	// DeletePrefix removes objects under prefix in bounded batches. It reports
	// remaining true when work is left, including when a delete was refused, so
	// a caller must keep calling until it reports false before recording the
	// artifact as gone.
	DeletePrefix(ctx context.Context, prefix string) (remaining bool, err error)
	Close()
}
type S3 struct {
	client    *s3.Client
	bucket    string
	transport *http.Transport
}

func NewS3(d Destination, c Credentials, blocked ...netip.Prefix) *S3 {
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second, MaxIdleConns: 2, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 2, IdleConnTimeout: 30 * time.Second, DisableCompression: true}
	if d.Project != "" {
		transport.Proxy = nil
		transport.DialContext = publicBackupDial(blocked)
	}
	client := s3.New(s3.Options{Region: d.Region, BaseEndpoint: aws.String(d.Endpoint), UsePathStyle: d.PathStyle, Credentials: credentials.NewStaticCredentialsProvider(c.AccessKeyID, c.SecretAccessKey, c.SessionToken), HTTPClient: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("object-store redirects are refused") }}, Retryer: retry.NewStandard(func(o *retry.StandardOptions) { o.MaxAttempts = 2 }), RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired, ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired})
	return &S3{client: client, bucket: d.Bucket, transport: transport}
}
func (s *S3) Close() { s.transport.CloseIdleConnections() }

// Put keeps exactly one 8 MiB part in memory. It completes a multipart upload
// only after the producer returns clean EOF, and aborts every known failure.
// Bucket lifecycle rules should also abort abandoned uploads after crashes.
func (s *S3) Put(ctx context.Context, key string, source io.Reader, limit int64) (total int64, digest string, err error) {
	if limit < 1 || limit > 64<<30 {
		return 0, "", fmt.Errorf("object size limit must be 1 byte–64 GiB")
	}
	buffer := make([]byte, PartSize)
	hash := sha256.New()
	reader := io.TeeReader(io.LimitReader(source, limit+1), hash)
	var uploadID string
	defer func() {
		if err != nil && uploadID != "" {
			cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_, _ = s.client.AbortMultipartUpload(cleanup, &s3.AbortMultipartUploadInput{Bucket: aws.String(s.bucket), Key: aws.String(key), UploadId: aws.String(uploadID)})
		}
	}()
	parts := make([]types.CompletedPart, 0, 16)
	for {
		n, readErr := io.ReadFull(reader, buffer)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return total, "", fmt.Errorf("backup producer failed")
		}
		total += int64(n)
		if total > limit {
			return total, "", fmt.Errorf("backup exceeds configured object size limit")
		}
		if err = ctx.Err(); err != nil {
			return total, "", err
		}
		if uploadID == "" && readErr != nil {
			_, err = s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: bytes.NewReader(buffer[:n]), ContentLength: aws.Int64(int64(n)), ContentType: aws.String("application/octet-stream"), IfNoneMatch: aws.String("*")})
			if err != nil {
				return total, "", fmt.Errorf("S3 object upload failed")
			}
			return total, hex.EncodeToString(hash.Sum(nil)), nil
		}
		if uploadID == "" {
			created, e := s.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(s.bucket), Key: aws.String(key), ContentType: aws.String("application/octet-stream")})
			if e != nil {
				return total, "", fmt.Errorf("S3 multipart creation failed")
			}
			uploadID = aws.ToString(created.UploadId)
			if uploadID == "" {
				return total, "", fmt.Errorf("S3 returned no multipart upload identifier")
			}
		}
		if n > 0 {
			partNo := int32(len(parts) + 1)
			if partNo > 10000 {
				return total, "", fmt.Errorf("multipart part limit exceeded")
			}
			part, e := s.client.UploadPart(ctx, &s3.UploadPartInput{Bucket: aws.String(s.bucket), Key: aws.String(key), UploadId: aws.String(uploadID), PartNumber: aws.Int32(partNo), Body: bytes.NewReader(buffer[:n]), ContentLength: aws.Int64(int64(n))})
			if e != nil {
				return total, "", fmt.Errorf("S3 multipart part upload failed")
			}
			parts = append(parts, types.CompletedPart{PartNumber: aws.Int32(partNo), ETag: part.ETag})
		}
		if readErr != nil {
			break
		}
	}
	_, err = s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{Bucket: aws.String(s.bucket), Key: aws.String(key), UploadId: aws.String(uploadID), MultipartUpload: &types.CompletedMultipartUpload{Parts: parts}, IfNoneMatch: aws.String("*")})
	if err != nil {
		return total, "", fmt.Errorf("S3 multipart completion failed")
	}
	uploadID = ""
	return total, hex.EncodeToString(hash.Sum(nil)), nil
}
func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	value, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, 0, fmt.Errorf("S3 object download failed")
	}
	return value.Body, aws.ToInt64(value.ContentLength), nil
}
func (s *S3) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return fmt.Errorf("S3 object deletion failed")
	}
	return nil
}

// ListPrefix pages with a bounded page size. A truncated page without a
// continuation token is an error rather than a silent end of the listing.
func (s *S3) ListPrefix(ctx context.Context, prefix string, token string) ([]string, string, error) {
	if prefix == "" {
		return nil, "", fmt.Errorf("object prefix is required")
	}
	input := &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(prefix), MaxKeys: aws.Int32(PrefixPageSize)}
	if token != "" {
		input.ContinuationToken = aws.String(token)
	}
	page, err := s.client.ListObjectsV2(ctx, input)
	if err != nil {
		return nil, "", fmt.Errorf("S3 prefix listing failed")
	}
	keys := make([]string, 0, len(page.Contents))
	for _, object := range page.Contents {
		if key := aws.ToString(object.Key); strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	if !aws.ToBool(page.IsTruncated) {
		return keys, "", nil
	}
	next := aws.ToString(page.NextContinuationToken)
	if next == "" {
		return keys, "", fmt.Errorf("S3 reported a truncated listing without a continuation token")
	}
	return keys, next, nil
}

// DeletePrefix relists from the start of the prefix on every batch, because
// deleted keys leave the listing. That makes an interrupted run resumable: a
// second call continues where the first stopped. It reports remaining true
// whenever anything might be left, so a caller never records a deletion as
// complete while the bucket still holds objects.
func (s *S3) DeletePrefix(ctx context.Context, prefix string) (bool, error) {
	if prefix == "" {
		return false, fmt.Errorf("object prefix is required")
	}
	for deleted := 0; deleted < MaxPrefixDeletePerCall; deleted += PrefixPageSize {
		keys, _, err := s.ListPrefix(ctx, prefix, "")
		if err != nil {
			return true, err
		}
		if len(keys) == 0 {
			return false, nil
		}
		objects := make([]types.ObjectIdentifier, 0, len(keys))
		for _, key := range keys {
			objects = append(objects, types.ObjectIdentifier{Key: aws.String(key)})
		}
		result, err := s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: aws.String(s.bucket), Delete: &types.Delete{Objects: objects, Quiet: aws.Bool(true)}})
		if err != nil {
			return true, fmt.Errorf("S3 prefix deletion failed")
		}
		if len(result.Errors) > 0 {
			return true, fmt.Errorf("S3 refused %d of %d objects under the backup prefix", len(result.Errors), len(objects))
		}
	}
	return true, nil
}

// destinationRoot is the one place the object namespace is assembled, so a
// single-object key and an engine prefix cannot drift apart.
func destinationRoot(d Destination) string {
	root := strings.TrimSuffix(d.Prefix, "/")
	if d.Prefix != "" {
		root += "/"
	}
	return root + "hakopod/"
}
func ObjectKey(d Destination, id string) string {
	return destinationRoot(d) + id + ".age"
}

// EnginePrefix locates the tree an engine-managed backup writes. It sits beside
// the single-object key for the same artifact and never overlaps it.
func EnginePrefix(d Destination, id string) string {
	return destinationRoot(d) + id + "/"
}
