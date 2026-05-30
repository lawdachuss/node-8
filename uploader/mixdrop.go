package uploader

import (
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// MixdropUploader handles uploading files to Mixdrop
type MixdropUploader struct {
	email  string
	token  string
	client *http.Client
}

// NewMixdropUploader creates a new Mixdrop uploader instance
func NewMixdropUploader(email, token string) *MixdropUploader {
	return &MixdropUploader{
		email: email,
		token: token,
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
			},
		},
	}
}

type mixdropUploadResp struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	Result  struct {
		Fileref string `json:"fileref"`
		Title   string `json:"title"`
		Name    string `json:"name"`
	} `json:"result"`
}

// Upload uploads a file to Mixdrop and returns the embed link
func (u *MixdropUploader) Upload(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	pipeReader, pipeWriter := io.Pipe()
	writer := multipart.NewWriter(pipeWriter)
	errCh := make(chan error, 1)

	go func() {
		defer func() {
			writer.Close()
			pipeWriter.Close()
		}()
		if err := writer.WriteField("email", u.email); err != nil {
			errCh <- fmt.Errorf("write email field: %w", err)
			pipeWriter.CloseWithError(err)
			return
		}
		if err := writer.WriteField("token", u.token); err != nil {
			errCh <- fmt.Errorf("write token field: %w", err)
			pipeWriter.CloseWithError(err)
			return
		}
		part, err := writer.CreateFormFile("file", filepath.Base(filePath))
		if err != nil {
			errCh <- fmt.Errorf("create form file: %w", err)
			pipeWriter.CloseWithError(err)
			return
		}
		buf := make([]byte, 1024*1024)
		if _, err := io.CopyBuffer(part, file, buf); err != nil {
			errCh <- fmt.Errorf("copy file: %w", err)
			pipeWriter.CloseWithError(err)
			return
		}
		errCh <- nil
	}()

	req, err := http.NewRequest("POST", "https://ul.mixdrop.ag/api", pipeReader)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("User-Agent", defaultUserAgent)

	// Fallback: include token in Authorization header if the API expects it there.
	if u.token != "" {
		req.Header.Set("Authorization", "Bearer "+u.token)
	}

	resp, err := u.client.Do(req)
	if err != nil {
		select {
		case goroutineErr := <-errCh:
			if goroutineErr != nil {
				return "", fmt.Errorf("multipart write: %w (request: %v)", goroutineErr, err)
			}
		default:
		}
		return "", fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("upload status %d: %s", resp.StatusCode, string(body))
	}

	var uploadResp mixdropUploadResp
	if err := json.NewDecoder(resp.Body).Decode(&uploadResp); err != nil {
		return "", fmt.Errorf("decode upload response: %w", err)
	}
	if !uploadResp.Success {
		return "", fmt.Errorf("upload failed: %s", uploadResp.Error)
	}
	if uploadResp.Result.Fileref == "" {
		return "", fmt.Errorf("empty fileref in upload response")
	}

	return fmt.Sprintf("https://mixdrop.ag/e/%s", uploadResp.Result.Fileref), nil
}
