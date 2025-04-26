package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"os/exec"

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

	copySize, err := io.Copy(tempFile, uploadFile)
	if err != nil {	
		respondWithError(w, http.StatusInternalServerError, "Unable to copy file", err)
		return
	}

	// Enforce size limit before transferring to S3
	// TODO: It would be better to do this in a streaming manner at multipart
	// form parsing time, assuming the uploader supplies a content-length
	if copySize > maxUploadSize {
		respondWithError(w, http.StatusBadRequest, "File too large", fmt.Errorf("upload_video: file size %d exceeds limit %d", copySize, maxUploadSize))
		return
	}

	// Get the aspect ratio of the video
	aspectRatio, err := getVideoAspectRatio(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to get video aspect ratio", err)
		return
	}

	// Determine the storage prefix based on the aspect ratio
	var storagePrefix string
	switch aspectRatio {
	case "16:9":
		storagePrefix = "landscape/"
	case "9:16":
		storagePrefix = "portrait/"
	default:
		storagePrefix = "other/"
	}

	// Generate a random filename
	randBytes := make([]byte, 32)
	// Guaranteed not to return an error on all but legacy Linux systems
	rand.Read(randBytes)
	// Use base64.RawURLEncoding to get a URL-safe string
	randString := base64.RawURLEncoding.EncodeToString(randBytes)
	fileName := storagePrefix + randString + fileExtension

	// Upload to S3
	tempFile.Seek(0, io.SeekStart) // Rewind the file
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

func getVideoAspectRatio(filePath string) (string, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)
	resultBuffer := bytes.Buffer{}
	cmd.Stdout = &resultBuffer
	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("ffprobe error: %v", err)
	}

	// Just extract the first stream's width and height
	type FFProbeResult struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
	}
	var ffprobeResult FFProbeResult

	// Parse the JSON output to get the aspect ratio
	err = json.Unmarshal(resultBuffer.Bytes(), &ffprobeResult)
	if err != nil {	
		return "", fmt.Errorf("json unmarshal error: %v", err)
	}
	if len(ffprobeResult.Streams) == 0 {
		return "", fmt.Errorf("no streams found in ffprobe output")
	}
	width := ffprobeResult.Streams[0].Width
	height := ffprobeResult.Streams[0].Height

	if height == 0 {
		return "", fmt.Errorf("height is zero, cannot calculate aspect ratio")
	}

	ratio := float64(width) / float64(height)

	if isEqualWithTolerance(ratio, 1.778, 0.001) {
		return "16:9", nil
	} else if isEqualWithTolerance(ratio, 0.5625, 0.001) {
		return "9:16", nil
	} else {
		return "other", nil
	}
}

func isEqualWithTolerance(a, b, tolerance float64) bool {
	return math.Abs(a - b) <= tolerance
}