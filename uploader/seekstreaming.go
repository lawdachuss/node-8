package uploader

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// seekStreamingSem limits concurrent uploads to SeekStreaming.
const seekStreamingSemCap = 3

var seekStreamingSem = make(chan struct{}, seekStreamingSemCap)

type SeekStreamingUploader struct {
	key    string
	client *http.Client
}

func NewSeekStreamingUploader(key string) *SeekStreamingUploader {
	return &SeekStreamingUploader{
		key: key,
		client: &http.Client{
			Timeout: 120 * time.Minute,
			Transport: &http.Transport{
				MaxIdleConns:          10,
				MaxIdleConnsPerHost:   2,
				IdleConnTimeout:       90 * time.Second,
				DisableCompression:    true,
				TLSHandshakeTimeout:   30 * time.Second,
				ResponseHeaderTimeout: 120 * time.Second,
				DisableKeepAlives:     true,
				DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
			},
		},
	}
}

type seekStreamingUploadEndpointResp struct {
	TusURL      string `json:"tusUrl"`
	AccessToken string `json:"accessToken"`
}

func (u *SeekStreamingUploader) Upload(filePath string) (string, error) {
	return u.UploadWithProgress(filePath, nil)
}

func (u *SeekStreamingUploader) UploadWithProgress(filePath string, progress ProgressFunc) (string, error) {
	seekStreamingSem <- struct{}{}
	defer func() { <-seekStreamingSem }()

	ep, err := u.getUploadEndpoint()
	if err != nil {
		return "", fmt.Errorf("seekstreaming: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if attempt > 1 {
			time.Sleep(uploadBackoff(attempt-2, lastErr))
		}

		uploadURL, err := u.createTUSUpload(ep, filePath)
		if err != nil {
			lastErr = fmt.Errorf("create tus upload: %w", err)
			if isUploadRateLimited(err) {
				time.Sleep(uploadBackoff(attempt, err))
				lastErr = nil
				continue
			}
			if attempt < 3 {
				continue
			}
			return "", lastErr
		}

		videoID, err := u.uploadFileTUS(uploadURL, filePath, progress)
		if err != nil {
			lastErr = fmt.Errorf("upload file: %w", err)
			if isUploadRateLimited(err) {
				time.Sleep(uploadBackoff(attempt, err))
				lastErr = nil
				continue
			}
			if attempt < 3 {
				continue
			}
			return "", lastErr
		}

		return fmt.Sprintf("https://chuglii.embedseek.com/#%s", videoID), nil
	}
	return "", lastErr
}

func (u *SeekStreamingUploader) getUploadEndpoint() (*seekStreamingUploadEndpointResp, error) {
	req, err := http.NewRequest("GET", "https://seekstreaming.com/api/v1/video/upload", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("api-token", u.key)
	req.Header.Set("User-Agent", defaultUserAgent)

	resp, err := u.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status 429: rate limit — %s", strings.TrimSpace(string(body)))
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var ep seekStreamingUploadEndpointResp
	if err := json.NewDecoder(resp.Body).Decode(&ep); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if ep.TusURL == "" || ep.AccessToken == "" {
		return nil, fmt.Errorf("empty tus URL or access token in response")
	}

	return &ep, nil
}

func (u *SeekStreamingUploader) createTUSUpload(ep *seekStreamingUploadEndpointResp, filePath string) (string, error) {
	fi, err := os.Stat(filePath)
	if err != nil {
		return "", fmt.Errorf("stat file: %w", err)
	}

	filename := filepath.Base(filePath)
	filetype := mimeTypeByExt(filepath.Ext(filename))

	b64 := func(s string) string {
		return base64.StdEncoding.EncodeToString([]byte(s))
	}

	metadata := fmt.Sprintf("accessToken %s,filename %s,filetype %s", b64(ep.AccessToken), b64(filename), b64(filetype))

	req, err := http.NewRequest("POST", ep.TusURL, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", fmt.Sprintf("%d", fi.Size()))
	req.Header.Set("Upload-Metadata", metadata)
	req.Header.Set("User-Agent", defaultUserAgent)

	resp, err := u.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("tus create: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("tus create status %d: %s", resp.StatusCode, string(body))
	}

	location := resp.Header.Get("Location")
	if location == "" {
		return "", fmt.Errorf("missing Location header in tus create response")
	}

	return location, nil
}

func (u *SeekStreamingUploader) uploadFileTUS(uploadURL, filePath string, progress ProgressFunc) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	fi, _ := os.Stat(filePath)
	fileSize := fi.Size()

	pr := NewProgressReaderWithCallback(f, fileSize, "SeekStreaming", progress)

	req, err := http.NewRequest("PATCH", uploadURL, pr)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Content-Type", "application/offset+octet-stream")
	req.Header.Set("Upload-Offset", "0")
	req.Header.Set("User-Agent", defaultUserAgent)

	resp, err := u.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("tus upload: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusNoContent {
		parts := strings.Split(strings.TrimRight(uploadURL, "/"), "/")
		return parts[len(parts)-1], nil
	}

	if resp.StatusCode == http.StatusOK {
		var result struct {
			VideoID string `json:"videoId"`
		}
		if err := json.Unmarshal(body, &result); err == nil && result.VideoID != "" {
			return result.VideoID, nil
		}
		parts := strings.Split(strings.TrimRight(uploadURL, "/"), "/")
		return parts[len(parts)-1], nil
	}

	return "", fmt.Errorf("tus upload status %d: %s", resp.StatusCode, string(body))
}

func ExtractSeekStreamingVideoID(embedURL string) string {
	if idx := strings.LastIndex(embedURL, "#"); idx >= 0 {
		return embedURL[idx+1:]
	}
	return ""
}

func GetSeekStreamingPosterURL(key, videoID string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("GET", fmt.Sprintf("https://seekstreaming.com/api/v1/video/manage/%s", videoID), nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("api-token", key)
	req.Header.Set("User-Agent", defaultUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}

	var detail struct {
		Poster   string `json:"poster"`
		AssetURL string `json:"assetUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if detail.Poster == "" || detail.AssetURL == "" {
		return "", fmt.Errorf("no poster available")
	}
	return detail.AssetURL + detail.Poster, nil
}

func mimeTypeByExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".mp4":
		return "video/mp4"
	case ".mkv":
		return "video/x-matroska"
	case ".webm":
		return "video/webm"
	case ".avi":
		return "video/x-msvideo"
	case ".mov":
		return "video/quicktime"
	case ".flv":
		return "video/x-flv"
	case ".ts":
		return "video/mp2t"
	case ".m4v":
		return "video/x-m4v"
	default:
		return "video/mp4"
	}
}
