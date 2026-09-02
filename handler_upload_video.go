package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
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

	aspectRatio, err := getVideoAspectRatio(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error getting aspect ratio", err)
		return
	}

	_, err = tempFile.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error using seek", err)
		return
	}

	outputPath, err := processVideoForFastStart(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error processing video", err)
		return
	}

	processedFile, err := os.Open(outputPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error creating processed video", err)
		return
	}
	defer processedFile.Close()

	key := make([]byte, 32)
	_, err = rand.Read(key)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error reading random", err)
		return
	}
	encodedURLString := base64.URLEncoding.EncodeToString(key)

	mediaTypeSplit := strings.Split(mediaType, "/")

	var filename string
	switch aspectRatio {
	case "16:9":
		filename = fmt.Sprintf("landscape/%v.%s", encodedURLString, mediaTypeSplit[len(mediaTypeSplit)-1])
	case "9:16":
		filename = fmt.Sprintf("portrait/%v.%s", encodedURLString, mediaTypeSplit[len(mediaTypeSplit)-1])
	default:
		filename = fmt.Sprintf("other/%v.%s", encodedURLString, mediaTypeSplit[len(mediaTypeSplit)-1])
	}

	_, err = cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &filename,
		Body:        processedFile,
		ContentType: &mediaType,
	})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error uploading object to S3", err)
		return
	}

	videoURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, filename)
	video.VideoURL = &videoURL
	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error updating video in database", err)
		return
	}

	respondWithJSON(w, http.StatusOK, "successfully uploaded video")
}

func getVideoAspectRatio(filepath string) (string, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filepath)

	var buf bytes.Buffer
	cmd.Stdout = &buf

	err := cmd.Run()
	if err != nil {
		return "", err
	}

	type Ratio struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	}

	type Data struct {
		Dimensions []Ratio `json:"streams"`
	}

	var data Data
	err = json.Unmarshal(buf.Bytes(), &data)
	if err != nil {
		return "", err
	}

	width := data.Dimensions[0].Width
	height := data.Dimensions[0].Height

	// 16:9
	expectedHeight := (width*9 + 8) / 16
	if height == expectedHeight {
		return "16:9", nil
	}

	// 9:16
	expectedWidth := (height*9 + 8) / 16
	if width == expectedWidth {
		return "9:16", nil
	}

	return "other", nil
}

func processVideoForFastStart(filepath string) (string, error) {
	outputPath := fmt.Sprintf("%s.processing", filepath)
	cmd := exec.Command("ffmpeg", "-i", filepath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", outputPath)

	if err := cmd.Run(); err != nil {
		return "", nil
	}

	return outputPath, nil
}
