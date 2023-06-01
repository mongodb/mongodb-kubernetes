package s3

import (
	"bytes"
	"context"
	"fmt"
	"net/http"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/google/uuid"
)

const (
	S3_REGION = "eu-west-2"
	S3_BUCKET = "kube-load-testing"
)

func NewS3Session() (*session.Session, error) {
	return session.NewSession(&aws.Config{Region: aws.String(S3_REGION)})
}

// UploadFile uploads the specified content to the key_prefix folder path in the s3 bucket
func UploadFile(ctx context.Context, s *session.Session, content, key_prefix string) error {
	buffer := []byte(content)
	// append with uuid to have unique file names for each test run
	suffix := uuid.New().String()

	_, err := s3.New(s).PutObject(&s3.PutObjectInput{
		Bucket:      aws.String(S3_BUCKET),
		Key:         aws.String(fmt.Sprintf("%s/%s", key_prefix, suffix)),
		ACL:         aws.String("private"),
		Body:        bytes.NewReader(buffer),
		ContentType: aws.String(http.DetectContentType(buffer)),
	})
	return err
}
