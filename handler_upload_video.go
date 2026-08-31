package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	closer := http.MaxBytesReader(w, r.Body, 1<<30)
	defer closer.Close()

	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "couldn't validate JWT", err)
		return
	}

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "couldn't find video by ID", err)
		return
	}

	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "video userID doesn't match validated userID", err)
		return
	}

	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "unable parsing video", err)
		return
	}
	defer file.Close()

	vMediaType := header.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType("video/mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "unable to parse media type", err)
		return
	}

	if vMediaType != mediaType {
		respondWithError(w, http.StatusBadRequest, "video must be an mp4", err)
		return
	}

	tempFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error storing video upload", err)
		return
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	_, err = io.Copy(tempFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error copying to temp file", err)
		return
	}
	_, err = tempFile.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error using seek", err)
		return
	}

	key := make([]byte, 32)
	_, err = rand.Read(key)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error reading random", err)
		return
	}
	encodedURLString := base64.URLEncoding.EncodeToString(key)

	mediaTypeSplit := strings.Split(mediaType, "/")
	filename := fmt.Sprintf("%v.%s", encodedURLString, mediaTypeSplit[len(mediaTypeSplit)-1])

	cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &filename,
		Body:        tempFile,
		ContentType: &mediaType,
	})

	videoURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, filename)
	video.VideoURL = &videoURL
	cfg.db.UpdateVideo(video)
}
