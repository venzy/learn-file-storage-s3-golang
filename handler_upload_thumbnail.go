package main

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/venzy/learn-file-storage-s3-golang/internal/auth"
	"github.com/venzy/learn-file-storage-s3-golang/internal/fileext"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
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
		respondWithError(w, http.StatusForbidden, "You are not allowed to upload a thumbnail for this video", nil)
		return
	}

	// Proceed with upload attempt
	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	const maxMemory = 10 << 20  // 10 MB
	r.ParseMultipartForm(maxMemory)

	// "thumbnail" should match the HTML form input name
	uploadFile, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer uploadFile.Close()

	// `uploadFile` is an `io.Reader` that we can read from to get the image data

	contentType := header.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse Content-Type", fmt.Errorf("upload_thumbnail: %s", err))
		return
	}
	if mediaType != "image/jpeg" && contentType != "image/png" {
		respondWithError(w, http.StatusBadRequest, "Invalid file type", fmt.Errorf("upload_thumbnail: expected image/jpeg or image/png, got %s", mediaType))
		return
	}

	// Write uploaded data to a file in assets directory
	// NOTE: Could use mime.ExtensionsByType(mediaType) and pick the first one,
	// but for now leave with our own little internal module
	fileExtension := fileext.FromMediaType(mediaType)
	if fileExtension == "" {
		// This is an internal error, as we restrict the content types above to a subset of those understood by fileext
		respondWithError(w, http.StatusInternalServerError, "Unrecognised Content-Type", fmt.Errorf("upload_thumbnail: unknown file extension for content type %s", contentType))
	}

	savePath := filepath.Join(cfg.assetsRoot, videoIDString + fileExtension)

	saveFile, err := os.Create(savePath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to create file", err)
		return
	}
	defer saveFile.Close()

	_, err = io.Copy(saveFile, uploadFile)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to save file", err)
		return
	}

	// Store path to file (handled by our assets file server)
	// NOTE: You wouldn't normally hardcode the hostname like this
	newURL := fmt.Sprintf("http://localhost:%s/assets/%s", cfg.port, videoIDString + fileExtension)
	videoMeta.ThumbnailURL = &newURL
	err = cfg.db.UpdateVideo(videoMeta)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to update video", err)
		return
	}

	respondWithJSON(w, http.StatusOK, videoMeta)
}
