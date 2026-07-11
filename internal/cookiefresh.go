package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/teacat/chaturbate-dvr/server"
)

const scraplingScript = `"""Grab cookies from chaturbate.com using Scrapling's StealthyFetcher."""
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

const scraplingTimeout = 120 * time.Second

type scraplingResult struct {
	Success bool              `json:"success"`
	Cookies map[string]string `json:"cookies"`
	Status  int               `json:"status"`
	Error   string            `json:"error"`
}

// runScrapling shells out to Python with the embedded Scrapling script to
// extract cookies through the given proxy. Uses a 120-second timeout
// (Scrapling's own timeout is 90s; this gives a buffer for process overhead).
// Returns the cookies as a map, or an error if extraction fails.
func runScrapling(ctx context.Context, proxyURL string) (map[string]string, error) {
	tmpFile, err := os.CreateTemp("", "scrapling_*.py")
	if err != nil {
		return nil, fmt.Errorf("cannot create temp script: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.WriteString(scraplingScript)
	tmpFile.Close()
	defer os.Remove(tmpPath)

	execCtx, execCancel := context.WithTimeout(ctx, scraplingTimeout)
	defer execCancel()

	cmd := exec.CommandContext(execCtx, "python", tmpPath, proxyURL)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// CommandContext kills the process when the context is done.
		switch {
		case ctx.Err() != nil:
			return nil, fmt.Errorf("scrapling cancelled: %w", ctx.Err())
		case execCtx.Err() != nil:
			return nil, fmt.Errorf("scrapling timed out after %s", scraplingTimeout)
		}
		// cmd may still have written JSON to stdout on failure
	}

	outStr := strings.TrimSpace(string(out))
	jsonStart := strings.Index(outStr, "{")
	if jsonStart < 0 {
		return nil, fmt.Errorf("no JSON in scrapling output: %s", outStr)
	}
	outStr = outStr[jsonStart:]

	var result scraplingResult
	if err := json.Unmarshal([]byte(outStr), &result); err != nil {
		return nil, fmt.Errorf("cannot parse scrapling output: %w — raw: %s", err, outStr)
	}

	if !result.Success {
		if result.Error != "" {
			return nil, fmt.Errorf("scrapling failed: %s", result.Error)
		}
		return nil, fmt.Errorf("scrapling returned success=false")
	}

	if len(result.Cookies) == 0 {
		return nil, fmt.Errorf("scrapling returned no cookies")
	}

	if v, ok := result.Cookies["cf_clearance"]; !ok || v == "" {
		return nil, fmt.Errorf("scrapling returned no cf_clearance (got %d cookies)", len(result.Cookies))
	}

	return result.Cookies, nil
}

// UpdateCookiesFromProxyContext extracts fresh cookies through the given
// proxy via Scrapling and updates server.Config under ConfigMu. The context
// can be used for cancellation (e.g. when the proxy rotates again).
func UpdateCookiesFromProxyContext(ctx context.Context, proxyURL string) error {
	cookies, err := runScrapling(ctx, proxyURL)
	if err != nil {
		return err
	}

	var parts []string
	for k, v := range cookies {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	cookieStr := strings.Join(parts, "; ")

	server.ConfigMu.Lock()
	defer server.ConfigMu.Unlock()

	server.Config.Cookies = cookieStr
	for k, v := range cookies {
		switch strings.ToLower(k) {
		case "sessionid":
			server.Config.SessionID = v
		case "csrftoken":
			server.Config.Csrftoken = v
		case "cf_clearance":
			server.Config.CfClearance = v
		}
	}

	return nil
}

// UpdateCookiesFromProxy is a convenience wrapper using context.Background.
func UpdateCookiesFromProxy(proxyURL string) error {
	return UpdateCookiesFromProxyContext(context.Background(), proxyURL)
}