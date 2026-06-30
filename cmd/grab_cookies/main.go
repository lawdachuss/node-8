package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sardanioss/httpcloak"
)

func main() {
	exitCode := 0
	defer func() { os.Exit(exitCode) }()

	loadDotEnv(".env")

	proxyURL := os.Getenv("PROXY_URL")
	if proxyURL == "" {
		proxyURL = os.Getenv("ALL_PROXY")
	}
	if idx := strings.Index(proxyURL, ","); idx > 0 {
		proxyURL = strings.TrimSpace(proxyURL[:idx])
	}

	userAgent := os.Getenv("USER_AGENT")
	if userAgent == "" {
		userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
	}
	oldCookieStr := os.Getenv("COOKIES")

	fmt.Println("=== Cookie Grabber ===")
	fmt.Printf("Proxy: %s\n", proxyURL)
	fmt.Printf("User-Agent: %s\n", userAgent)
	fmt.Printf("Existing cookies: %d chars\n", len(oldCookieStr))
	fmt.Println()

	// Verify proxy first
	if proxyURL != "" {
		fmt.Println("Verifying proxy...")
		egress := checkProxyEgress(proxyURL)
		if egress != "" {
			fmt.Printf("  Proxy OK — egress IP: %s\n", egress)
		} else {
			fmt.Println("  [WARN] Proxy test failed — proxy may be down")
		}
		fmt.Println()
	}

	// Track best attempt — if we get any cookies (even without fresh cf_clearance),
	// save them so __cf_bm gets refreshed.
	var bestCookies map[string]string

	// Phase 1: httpcloak without old cookies
	fmt.Println("[1/3] httpcloak (no cookies)...")
	for attempt := 0; attempt < 3; attempt++ {
		cookies := tryHTTPCloak(proxyURL, userAgent, "")
		if cookies != nil {
			if v, ok := cookies["cf_clearance"]; ok && v != "" {
				fmt.Println("  Fresh cf_clearance obtained!")
				saveAndExit(cookies, oldCookieStr, userAgent)
				return
			}
			if bestCookies == nil {
				bestCookies = cookies
			}
		}
		if attempt < 2 {
			fmt.Printf("  Retrying (%d/3)...\n", attempt+2)
			time.Sleep(3 * time.Second)
		}
	}

	// Phase 2: httpcloak with old cookies (better chance of getting cf_clearance)
	fmt.Println("[2/3] httpcloak (with existing cookies)...")
	for attempt := 0; attempt < 3; attempt++ {
		cookies := tryHTTPCloak(proxyURL, userAgent, oldCookieStr)
		if cookies != nil {
			if v, ok := cookies["cf_clearance"]; ok && v != "" {
				fmt.Println("  Fresh cf_clearance obtained!")
				saveAndExit(cookies, oldCookieStr, userAgent)
				return
			}
			if bestCookies == nil {
				bestCookies = cookies
			}
		}
		if attempt < 2 {
			fmt.Printf("  Retrying (%d/3)...\n", attempt+2)
			time.Sleep(3 * time.Second)
		}
	}

	// Phase 3: Go default client (HTTP proxy only)
	fmt.Println("[3/3] Go default HTTP client...")
	cookies := tryDefaultClient(proxyURL, userAgent, oldCookieStr)
	if cookies != nil {
		bestCookies = cookies
	}

	if bestCookies != nil {
		fmt.Println("\nNo fresh cf_clearance from Cloudflare (old one still valid), but refreshing __cf_bm and other session cookies")
		saveAndExit(bestCookies, oldCookieStr, userAgent)
		return
	}

	fmt.Println("\n[FAIL] Could not obtain cookies from any method")
	exitCode = 1
}

func saveAndExit(cookies map[string]string, oldCookieStr, userAgent string) {
	merged := parseCookies(oldCookieStr)
	for k, v := range cookies {
		merged[k] = v
	}

	var parts []string
	for k, v := range merged {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	newCookieStr := strings.Join(parts, "; ")

	updateEnvFile(".env", "COOKIES", newCookieStr)
	if userAgent != "" {
		updateEnvFile(".env", "USER_AGENT", userAgent)
	}

	fmt.Println("\n=== COOKIES UPDATED ===")
	if v, ok := cookies["cf_clearance"]; ok {
		fmt.Printf("cf_clearance: fresh! (timestamp: %s)\n", extractTimestamp(v))
	} else {
		fmt.Println("cf_clearance: unchanged (still valid)")
	}
	if v, ok := cookies["__cf_bm"]; ok {
		fmt.Printf("__cf_bm: fresh! (timestamp: %s)\n", extractTimestamp(v))
	}
	fmt.Printf("\nTotal cookies: %d\n", len(merged))
}

func tryHTTPCloak(proxyURL, userAgent, cookieStr string) map[string]string {
	opts := []httpcloak.Option{
		httpcloak.WithTimeout(60 * time.Second),
	}
	if proxyURL != "" {
		opts = append(opts, httpcloak.WithProxy(proxyURL))
	} else {
		fmt.Println("  No proxy configured, trying direct...")
	}

	client := httpcloak.New("chrome-146-windows", opts...)
	if c, ok := interface{}(client).(interface{ Close() error }); ok {
		defer c.Close()
	}

	// Visit chaturbate.com
	headers := map[string][]string{
		"User-Agent": {userAgent},
		"Accept":     {"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"},
	}
	if cookieStr != "" {
		headers["Cookie"] = []string{cookieStr}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := client.Do(ctx, &httpcloak.Request{
		Method:  "GET",
		URL:     "https://chaturbate.com",
		Headers: headers,
		Timeout: 60 * time.Second,
	})
	if err != nil {
		fmt.Printf("  httpcloak error: %v\n", err)
		return nil
	}
	fmt.Printf("  Status: %d\n", resp.StatusCode)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	cookies := extractCookies(resp.Headers)
	if len(cookies) == 0 {
		return nil
	}

	fmt.Printf("  Got %d cookies\n", len(cookies))
	for k, v := range cookies {
		if k == "cf_clearance" || k == "__cf_bm" {
			fmt.Printf("    %s = ...%s (ts: %s)\n", k, truncate(v, 20), extractTimestamp(v))
		}
	}

	// If no cf_clearance, try a second URL to trigger challenge
	if _, has := cookies["cf_clearance"]; !has || cookies["cf_clearance"] == "" {
		fmt.Println("  No cf_clearance — visiting auth page to trigger challenge...")
		time.Sleep(2 * time.Second)
		c2 := visitURL(client, userAgent, cookieStr, "https://chaturbate.com/auth/login/?next=/")
		if c2 != nil {
			for k, v := range c2 {
				if k == "cf_clearance" && v != "" {
					cookies[k] = v
					fmt.Println("  Got cf_clearance from auth page!")
				}
			}
		}
	}

	return cookies
}

func visitURL(client *httpcloak.Client, userAgent, cookieStr, targetURL string) map[string]string {
	headers := map[string][]string{
		"User-Agent": {userAgent},
		"Accept":     {"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"},
	}
	if cookieStr != "" {
		headers["Cookie"] = []string{cookieStr}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := client.Do(ctx, &httpcloak.Request{
		Method:  "GET",
		URL:     targetURL,
		Headers: headers,
		Timeout: 30 * time.Second,
	})
	if err != nil {
		fmt.Printf("  secondary request error: %v\n", err)
		return nil
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	fmt.Printf("  Status: %d\n", resp.StatusCode)
	return extractCookies(resp.Headers)
}

func extractCookies(headers map[string][]string) map[string]string {
	cookies := make(map[string]string)
	for key, vals := range headers {
		if strings.EqualFold(key, "Set-Cookie") {
			for _, setCookie := range vals {
				if idx := strings.Index(setCookie, "="); idx > 0 {
					name := setCookie[:idx]
					rest := setCookie[idx+1:]
					if idx2 := strings.Index(rest, ";"); idx2 > 0 {
						cookies[name] = rest[:idx2]
					} else {
						cookies[name] = rest
					}
				}
			}
		}
	}
	return cookies
}

func tryDefaultClient(proxyURL, userAgent, cookieStr string) map[string]string {
	jar, _ := cookiejar.New(nil)
	tr := &http.Transport{
		MaxIdleConns:      1,
		IdleConnTimeout:   30 * time.Second,
		DisableKeepAlives: true,
	}

	if strings.HasPrefix(proxyURL, "http://") || strings.HasPrefix(proxyURL, "https://") {
		proxyURLParsed, err := url.Parse(proxyURL)
		if err == nil {
			tr.Proxy = http.ProxyURL(proxyURLParsed)
		}
	} else if strings.HasPrefix(proxyURL, "socks5://") || strings.HasPrefix(proxyURL, "socks5h://") {
		fmt.Println("  SOCKS5 not supported by Go default transport")
		return nil
	}

	client := &http.Client{
		Timeout:   60 * time.Second,
		Transport: tr,
		Jar:       jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return nil
		},
	}

	req, err := http.NewRequest("GET", "https://chaturbate.com", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	if cookieStr != "" {
		req.Header.Set("Cookie", cookieStr)
	}

	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("  default client error: %v\n", err)
		return nil
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	fmt.Printf("  Status: %d\n", resp.StatusCode)

	cookies := make(map[string]string)
	for _, c := range jar.Cookies(req.URL) {
		cookies[c.Name] = c.Value
	}
	for _, setCookie := range resp.Header.Values("Set-Cookie") {
		if idx := strings.Index(setCookie, "="); idx > 0 {
			name := setCookie[:idx]
			rest := setCookie[idx+1:]
			if idx2 := strings.Index(rest, ";"); idx2 > 0 {
				cookies[name] = rest[:idx2]
			} else {
				cookies[name] = rest
			}
		}
	}
	if len(cookies) > 0 {
		fmt.Printf("  Got %d cookies\n", len(cookies))
		return cookies
	}
	return nil
}

// checkProxyEgress tests the proxy by connecting to api.ipify.org and
// returning our egress IP. Returns empty string if proxy fails.
func checkProxyEgress(proxyURL string) string {
	opts := []httpcloak.Option{
		httpcloak.WithTimeout(15 * time.Second),
		httpcloak.WithProxy(proxyURL),
	}
	client := httpcloak.New("chrome-146-windows", opts...)
	if c, ok := interface{}(client).(interface{ Close() error }); ok {
		defer c.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, err := client.Do(ctx, &httpcloak.Request{
		Method: "GET",
		URL:    "https://api.ipify.org",
		Timeout: 15 * time.Second,
	})
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return strings.TrimSpace(string(body))
}

// ─── helpers ───────────────────────────────────────────────

func parseCookies(s string) map[string]string {
	m := make(map[string]string)
	for _, pair := range strings.Split(s, ";") {
		pair = strings.TrimSpace(pair)
		if idx := strings.Index(pair, "="); idx > 0 {
			m[pair[:idx]] = pair[idx+1:]
		}
	}
	return m
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func extractTimestamp(cfClearance string) string {
	idx := strings.Index(cfClearance, "-")
	if idx < 0 {
		return "unknown"
	}
	tsStr := cfClearance[idx+1:]
	if idx2 := strings.Index(tsStr, "-"); idx2 >= 0 {
		tsStr = tsStr[:idx2]
	}
	return fmt.Sprintf("%s (%s)", tsStr, time.Unix(parseInt64(tsStr), 0).Format(time.RFC3339))
}

func parseInt64(s string) int64 {
	var n int64
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			n = n*10 + int64(s[i]-'0')
		} else {
			break
		}
	}
	return n
}

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
