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

// PixeldrainUploader handles uploading files to PixelDrain
type PixeldrainUploader struct {
	token  string
	client *http.Client
}

// NewPixeldrainUploader creates a new Pixeldrain uploader instance
func NewPixeldrainUploader(token string) *PixeldrainUploader {
	return &PixeldrainUploader{
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

// Upload uploads a file to PixelDrain and returns the view/download link
func (u *PixeldrainUploader) Upload(filePath string) (string, error) {
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
		if u.token != "" {
			// some integrations expect a token field; harmless if ignored
			if err := writer.WriteField("token", u.token); err != nil {
				errCh <- fmt.Errorf("write token field: %w", err)
				pipeWriter.CloseWithError(err)
				return
			}
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

	req, err := http.NewRequest("POST", "https://pixeldrain.com/api/file", pipeReader)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("User-Agent", defaultUserAgent)

	// PixelDrain expects the API key in the HTTP Basic Auth password field.
	// Use an empty username and the API key as the password.
	if u.token != "" {
		req.SetBasicAuth("", u.token)
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

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("upload status %d: %s", resp.StatusCode, string(body))
	}

	var out map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode upload response: %w", err)
	}

	// Attempt common response fields
	if id, ok := out["id"]; ok {
		return fmt.Sprintf("https://pixeldrain.com/u/%v", id), nil
	}
	if id, ok := out["file_id"]; ok {
		return fmt.Sprintf("https://pixeldrain.com/u/%v", id), nil
	}
	if url, ok := out["url"]; ok {
		return fmt.Sprintf("%v", url), nil
	}
	if link, ok := out["link"]; ok {
		return fmt.Sprintf("%v", link), nil
	}
	if result, ok := out["result"].(map[string]interface{}); ok {
		if id, ok := result["id"]; ok {
			return fmt.Sprintf("https://pixeldrain.com/u/%v", id), nil
		}
		if url, ok := result["url"]; ok {
			return fmt.Sprintf("%v", url), nil
		}
	}

	return "", fmt.Errorf("unexpected upload response: %+v", out)
}
