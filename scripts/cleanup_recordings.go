//go:build ignore

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var (
	supabaseURL    string
	supabaseAPIKey string
	httpClient     = &http.Client{Timeout: 60 * time.Second}
)

func loadEnv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		k := strings.TrimSpace(parts[0])
		v := strings.TrimSpace(parts[1])
		v = strings.Trim(v, `"'`)
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

func main() {
	loadEnv(".env")
	supabaseURL = os.Getenv("SUPABASE_URL")
	supabaseAPIKey = os.Getenv("SUPABASE_API_KEY")

	minDur := flag.Int("min-duration", 1200, "Delete recordings shorter than this (seconds, 0 = disable)")
	maxDur := flag.Int("max-duration", 10800, "Delete recordings longer than this (seconds, 0 = disable)")
	deleteZeroSize := flag.Bool("delete-zero-size", true, "Delete recordings with filesize = 0 (corrupted)")
	dryRun := flag.Bool("dry-run", true, "Only list matching recordings, do not delete")
	flag.Parse()

	if supabaseURL == "" || supabaseAPIKey == "" {
		fmt.Println("ERROR: SUPABASE_URL and SUPABASE_API_KEY must be set in environment or .env")
		os.Exit(1)
	}

	if *minDur <= 0 && *maxDur <= 0 && !*deleteZeroSize {
		fmt.Println("No criteria enabled — nothing to do.")
		return
	}

	all, err := fetchAllRecordings()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Total recordings in DB: %d\n", len(all))

	var toDelete []Recording
	var reasons []string

	for _, r := range all {
		var match bool
		var why string

		if *minDur > 0 && r.Duration > 0 && r.Duration < float64(*minDur) {
			match = true
			why = fmt.Sprintf("short (%.1fs < %ds)", r.Duration, *minDur)
		}
		if *maxDur > 0 && r.Duration > 0 && r.Duration > float64(*maxDur) {
			match = true
			why = fmt.Sprintf("long (%.1fs > %ds)", r.Duration, *maxDur)
		}
		if *deleteZeroSize && r.Filesize == 0 && r.Duration > 0 {
			match = true
			why = fmt.Sprintf("zero-size (%.1fs)", r.Duration)
		}
		if *deleteZeroSize && r.Filesize == 0 && r.Duration == 0 {
			match = true
			why = "zero-size + no duration (corrupted)"
		}

		if match {
			toDelete = append(toDelete, r)
			reasons = append(reasons, why)
		}
	}

	if len(toDelete) == 0 {
		fmt.Println("No matching recordings found.")
		return
	}

	fmt.Printf("\nFound %d recording(s) to delete:\n", len(toDelete))
	for i, r := range toDelete {
		fmt.Printf("  %-50s  %s\n", r.Filename, reasons[i])
	}

	if *dryRun {
		fmt.Println("\nDry-run mode — nothing deleted. Pass -dry-run=false to actually delete.")
		return
	}

	fmt.Print("\nProceed with deletion? (y/N): ")
	var confirm string
	fmt.Scanln(&confirm)
	if strings.ToLower(strings.TrimSpace(confirm)) != "y" {
		fmt.Println("Aborted.")
		return
	}

	fmt.Println("\nDeleting...")
	var deleted, errs int
	for _, r := range toDelete {
		if err := deleteRecordingCompletely(r); err != nil {
			fmt.Fprintf(os.Stderr, "  FAIL: %s — %v\n", r.Filename, err)
			errs++
		} else {
			fmt.Printf("  DELETED: %s\n", r.Filename)
			deleted++
		}
	}
	fmt.Printf("\nDone: %d deleted, %d errors\n", deleted, errs)
}

type Recording struct {
	ID        string  `json:"id"`
	Filename  string  `json:"filename"`
	Duration  float64 `json:"duration"`
	Filesize  int64   `json:"filesize"`
	Timestamp string  `json:"timestamp"`
}

func supabaseRequest(method, path string, body []byte) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = strings.NewReader(string(body))
	}
	req, err := http.NewRequest(method, supabaseURL+"/rest/v1"+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("apikey", supabaseAPIKey)
	req.Header.Set("Authorization", "Bearer "+supabaseAPIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Prefer", "resolution=merge-duplicates,return=minimal")
	}
	return httpClient.Do(req)
}

func fetchAllRecordings() ([]Recording, error) {
	var all []Recording
	offset := 0
	pageSize := 1000

	for {
		var page []Recording
		path := fmt.Sprintf("/recordings?select=id,filename,duration,filesize,timestamp&order=timestamp.desc&limit=%d&offset=%d", pageSize, offset)
		resp, err := supabaseRequest("GET", path, nil)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
		}
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("decode: %w", err)
		}
		resp.Body.Close()
		all = append(all, page...)
		if len(page) < pageSize {
			break
		}
		offset += pageSize
	}
	return all, nil
}

func deleteRecordingCompletely(r Recording) error {
	if r.ID != "" {
		path := fmt.Sprintf("/upload_links?recording_id=eq.%s", url.QueryEscape(r.ID))
		resp, err := supabaseRequest("DELETE", path, nil)
		if err != nil {
			return fmt.Errorf("delete upload links: %w", err)
		}
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			b, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("delete upload links HTTP %d: %s", resp.StatusCode, string(b))
		}
	}

	path := fmt.Sprintf("/preview_images?filename=eq.%s", url.QueryEscape(r.Filename))
	resp, err := supabaseRequest("DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("delete preview images: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete preview images HTTP %d: %s", resp.StatusCode, string(b))
	}

	path = fmt.Sprintf("/recordings?filename=eq.%s", url.QueryEscape(r.Filename))
	resp, err = supabaseRequest("DELETE", path, nil)
	if err != nil {
		return fmt.Errorf("delete recording: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete recording HTTP %d: %s", resp.StatusCode, string(b))
	}

	return nil
}
