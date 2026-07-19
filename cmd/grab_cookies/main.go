package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	exitCode := 0
	defer func() { os.Exit(exitCode) }()

	loadDotEnv(".env")

	userAgent := os.Getenv("USER_AGENT")
	if userAgent == "" {
		userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
	}

	fmt.Println("=== Cookie Grabber (Scrapling) ===")
	fmt.Println()

	envProxies := getProxyURLs()
	if len(envProxies) == 0 {
		fmt.Println("No PROXY_URL set")
		exitCode = 1
		return
	}

	// Write Python script to temp file once
	tmpFile, err := os.CreateTemp("", "grab_cookies_*.py")
	if err != nil {
		fmt.Printf("[FAIL] Cannot create temp script: %v\n", err)
		exitCode = 1
		return
	}
	tmpPath := tmpFile.Name()
	tmpFile.WriteString(pythonScript)
	tmpFile.Close()
	defer os.Remove(tmpPath)

	// Try each proxy until one works
	for i, proxyURL := range envProxies {
		fmt.Printf("Proxy %d/%d: %s\n", i+1, len(envProxies), proxyURL)

		cookies, ok := runScrapling(tmpPath, proxyURL)
		if !ok {
			fmt.Println()
			continue
		}

		// Must have cf_clearance
		if v, found := cookies["cf_clearance"]; !found || v == "" {
			fmt.Println("[SKIP] No cf_clearance from this proxy")
			continue
		}

		saveAndExit(cookies, userAgent)
		return
	}

	fmt.Println("[FAIL] All proxies failed — preserving old cookies from secret")
	exitCode = 1
}

func runScrapling(tmpPath, proxyURL string) (map[string]string, bool) {
	fmt.Println("  Running Scrapling browser (solving Cloudflare challenge, 180s timeout)...")
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python", tmpPath, proxyURL)
	stdout := &strings.Builder{}
	stderr := &strings.Builder{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Run()
	if ctx.Err() != nil {
		fmt.Printf("  [FAIL] Scrapling timed out after 180s\n")
		return nil, false
	}
	if stderr.Len() > 0 {
		for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				fmt.Printf("  %s\n", line)
			}
		}
	}
	outStr := stdout.String()

	// Find JSON in output
	jsonStart := strings.Index(outStr, "{")
	if jsonStart >= 0 {
		outStr = outStr[jsonStart:]
	} else {
		jsonStart = strings.Index(outStr, "[")
		if jsonStart >= 0 {
			outStr = outStr[jsonStart:]
		}
	}

	var result struct {
		Success bool              `json:"success"`
		Cookies map[string]string `json:"cookies"`
		Status  int               `json:"status"`
	}
	if json.Unmarshal([]byte(outStr), &result) != nil || !result.Success {
		fmt.Printf("  [FAIL] Scrapling error\n  Raw: %s\n", strings.TrimSpace(outStr))
		return nil, false
	}

	if len(result.Cookies) == 0 {
		fmt.Println("  [FAIL] No cookies returned")
		return nil, false
	}

	fmt.Printf("  Status: %d | Cookies: %d", result.Status, len(result.Cookies))
	if v, ok := result.Cookies["cf_clearance"]; ok {
		fmt.Printf(" | cf_clearance: len=%d", len(v))
	}
	fmt.Println()
	return result.Cookies, true
}

func saveAndExit(cookies map[string]string, userAgent string) {
	var parts []string
	for k, v := range cookies {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	cookieStr := strings.Join(parts, "; ")

	updateEnvFile(".env", "COOKIES", cookieStr)
	if userAgent != "" {
		updateEnvFile(".env", "USER_AGENT", userAgent)
	}

	fmt.Println("\n=== COOKIES UPDATED ===")
	fmt.Printf("Total cookies: %d\n", len(cookies))
}

// getProxyURLs returns all proxy URLs from the environment.
func getProxyURLs() []string {
	raw := os.Getenv("PROXY_URL")
	if raw == "" {
		raw = os.Getenv("ALL_PROXY")
	}
	if raw == "" {
		return nil
	}
	var urls []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			urls = append(urls, part)
		}
	}
	return urls
}

// ─── helpers ───────────────────────────────────────────────

func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		exe, err2 := os.Executable()
		if err2 == nil {
			f, err = os.Open(filepath.Join(filepath.Dir(exe), path))
		}
		if err != nil {
			return
		}
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
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

func updateEnvFile(path, key, value string) {
	data, err := os.ReadFile(path)
	if err != nil {
		entry := fmt.Sprintf("%s=\"%s\"\n", key, value)
		os.WriteFile(path, []byte(entry), 0644)
		fmt.Printf("  [OK] Created %s in %s\n", key, path)
		return
	}

	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == key {
			lines[i] = fmt.Sprintf("%s=\"%s\"", key, value)
			found = true
			break
		}
	}

	if !found {
		lines = append(lines, fmt.Sprintf("%s=\"%s\"", key, value))
	}

	output := strings.Join(lines, "\n")
	if err := os.WriteFile(path, []byte(output), 0644); err != nil {
		fmt.Printf("  [WARN] Failed to write %s: %v\n", path, err)
		return
	}
	fmt.Printf("  [OK] Updated %s in %s\n", key, path)
}

const pythonScript = `"""Grab cookies from chaturbate.com using Scrapling's StealthyFetcher."""
import json,sys,os,logging
logging.basicConfig(level=logging.INFO, format="[COOKIE] %(message)s")
log=logging.getLogger()
log.info("=== Cookie Grabber Started ===")
log.info("Proxy: %s", sys.argv[1] if len(sys.argv)>1 else "none")
from scrapling.fetchers import StealthyFetcher
proxy=sys.argv[1] if len(sys.argv)>1 else None
log.info("Launching headless browser...")
resp=StealthyFetcher.fetch(
 "https://chaturbate.com",
 headless=True,
 disable_resources=True,
 network_idle=False,
 solve_cloudflare=True,
 timeout=120000,
 proxy=proxy,
 load_dom=False,
 block_webrtc=True,
 retries=3,
 retry_delay=2
)
log.info("Page loaded (HTTP %d), extracting cookies...", resp.status)
cookies={}
if isinstance(resp.cookies,tuple):
 for c in resp.cookies:
  if isinstance(c,dict)and"name"in c:
   cookies[c["name"]]=c["value"]
  elif isinstance(c,dict):
   cookies.update(c)
elif isinstance(resp.cookies,dict):
 cookies=dict(resp.cookies)
log.info("Got %d cookies", len(cookies))
if "cf_clearance" in cookies:
 log.info("cf_clearance found! length=%d", len(cookies["cf_clearance"]))
else:
 log.warning("No cf_clearance cookie!")
print(json.dumps({"success":True,"cookies":cookies,"status":resp.status}))
log.info("=== Cookie Grabber Complete ===")
`
