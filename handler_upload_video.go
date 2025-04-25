package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/venzy/learn-file-storage-s3-golang/internal/auth"
	"github.com/venzy/learn-file-storage-s3-golang/internal/fileext"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	const maxUploadSize = 1 << 30 // 1GB

	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	// Authenticate the user
	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	// Check authorisation
	videoMeta, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to get video", err)
		return
	}
	if videoMeta.UserID != userID {
		respondWithError(w, http.StatusForbidden, "You are not allowed to upload content for this video", nil)
		return
	}

	// Proceed with upload attempt
	fmt.Println("uploading content for video", videoID, "by user", userID)

	// Receive upload and spill anything > maxMemory to temporary files
	// TODO I think this means we end up with two lots of temporary files; is
	// there a way of using e.g. MultipartReader to stream better?
	const maxMemory = 10 << 20  // 10 MB
	r.ParseMultipartForm(maxMemory)

	// "video" should match the HTML form input name
	uploadFile, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer uploadFile.Close()

	// `uploadFile` is an `io.Reader` that we can read from to get the file data

	contentType := header.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse Content-Type", fmt.Errorf("upload_video: %s", err))
		return
	}
	if mediaType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Invalid file type", fmt.Errorf("upload_video: expected video/mp4, got %s", mediaType))
		return
	}

	// NOTE: Could use mime.ExtensionsByType(mediaType) and pick the first one,
	// but for now leave with our own little internal module
	fileExtension := fileext.FromMediaType(mediaType)
	if fileExtension == "" {
		// This is an internal error, as we restrict the content types above to a subset of those understood by fileext
		respondWithError(w, http.StatusInternalServerError, "Unrecognised Content-Type", fmt.Errorf("upload_thumbnail: unknown file extension for content type %s", contentType))
	}

	// Save as a temporary file
	tempFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to create temporary file", err)
		return
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	_, err = io.Copy(tempFile, uploadFile)
	if err != nil {	
		respondWithError(w, http.StatusInternalServerError, "Unable to copy file", err)
		return
	}

	tempFile.Seek(0, io.SeekStart) // Rewind the file

	// Generate a random filename
	randBytes := make([]byte, 32)
	// Guaranteed not to return an error on all but legacy Linux systems
	rand.Read(randBytes)
	// Use base64.RawURLEncoding to get a URL-safe string
	randString := base64.RawURLEncoding.EncodeToString(randBytes)
	fileName := randString + fileExtension

	// Upload to S3
	_, err = cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket: aws.String(cfg.s3Bucket),
		Key:    aws.String(fileName),  // Use the filename directly as the key
		Body:   tempFile,
		ContentType: aws.String(mediaType),
	})

	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to upload file", err)
		return
	}

	// Update the database with the S3 URL
	s3URL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, fileName)
	videoMeta.VideoURL = &s3URL
	err = cfg.db.UpdateVideo(videoMeta)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to update video", err)
		return
	}

	// Respond with updated video metadata
	respondWithJSON(w, http.StatusOK, videoMeta)
}
