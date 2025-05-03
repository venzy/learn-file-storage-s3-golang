package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/venzy/learn-file-storage-s3-golang/internal/database"
)

func generatePresignedURL(s3Client *s3.Client, bucket, key string, expireTime time.Duration) (string, error) {
	presignClient := s3.NewPresignClient(s3Client)
	presignedRequest, err := presignClient.PresignGetObject(
		context.Background(),
		&s3.GetObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
		},
		s3.WithPresignExpires(expireTime),
	)
	if err != nil {
		return "", fmt.Errorf("failed to presign request: %w", err)
	}
	return presignedRequest.URL, nil
}

func (cfg *apiConfig) dbVideoToSignedVideo(video database.Video) (database.Video, error) {
	if video.VideoURL == nil {
		return video, nil
	}

	videoURLParts := strings.Split(*video.VideoURL, ",")
	if len(videoURLParts) != 2 {
		return video, fmt.Errorf("invalid video URL format")
	}
	bucket := videoURLParts[0]
	key := videoURLParts[1]
	// Check if the bucket matches the configured bucket
	if bucket != cfg.s3Bucket {
		return video, fmt.Errorf("bucket mismatch: expected %s, got %s", cfg.s3Bucket, bucket)
	}
	// Check if the key is valid
	if key == "" {
		return video, fmt.Errorf("invalid key: empty string")
	}

	// Generate a presigned URL for the video
	presignedURL, err := generatePresignedURL(cfg.s3Client, bucket, key, 15*time.Minute)
	if err != nil {
		return video, fmt.Errorf("failed to generate presigned URL: %w", err)
	}

	video.VideoURL = &presignedURL
	return video, nil
}