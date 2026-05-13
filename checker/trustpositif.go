package checker

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// trustPositifClient dipakai khusus untuk scraping trustpositif.komdigi.go.id
var trustPositifClient = &http.Client{
	Timeout: 20 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return nil
	},
}

func checkTrustPositif(domain string) (string, error) {
	baseURL := "https://trustpositif.komdigi.go.id/"

	// 1) Ambil halaman awal untuk dapatkan CSRF token
	req, err := http.NewRequest("GET", baseURL, nil)
	if err != nil {
		return "", err
	}
	setTrustPositifHeaders(req)

	resp, err := trustPositifClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	csrfToken := extractCSRFToken(string(bodyBytes))
	if csrfToken == "" {
		return "", fmt.Errorf("csrf_token tidak ketemu")
	}

	// 2) Request ke endpoint hasil dengan CSRF token
	checkURL := fmt.Sprintf(
		"https://trustpositif.komdigi.go.id/welcome?csrf_token=%s&recaptcha_token=&domains=%s",
		url.QueryEscape(csrfToken),
		url.QueryEscape(domain),
	)

	req2, err := http.NewRequest("GET", checkURL, nil)
	if err != nil {
		return "", err
	}
	setTrustPositifHeaders(req2)
	req2.Header.Set("Referer", baseURL)

	resp2, err := trustPositifClient.Do(req2)
	if err != nil {
		return "", err
	}
	defer resp2.Body.Close()

	resultBody, err := io.ReadAll(resp2.Body)
	if err != nil {
		return "", err
	}

	// 3) Parse HTML hasil
	return parseStatusFromHTML(string(resultBody), domain)
}

func setTrustPositifHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "id-ID,id;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("Connection", "keep-alive")
}

func extractCSRFToken(html string) string {
	re := regexp.MustCompile(`csrf_token=([a-fA-F0-9]+)`)
	match := re.FindStringSubmatch(html)
	if len(match) > 1 {
		return match[1]
	}
	re2 := regexp.MustCompile(`csrf_token["'\s:=]+([a-fA-F0-9]+)`)
	match2 := re2.FindStringSubmatch(html)
	if len(match2) > 1 {
		return match2[1]
	}
	return ""
}

func parseStatusFromHTML(html string, domain string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", err
	}

	var foundStatus string
	doc.Find("table tr").Each(func(i int, s *goquery.Selection) {
		tds := s.Find("td")
		if tds.Length() >= 2 {
			col1 := strings.TrimSpace(tds.Eq(0).Text())
			col2 := strings.TrimSpace(tds.Eq(1).Text())
			if normalizeDomainStr(col1) == normalizeDomainStr(domain) {
				foundStatus = col2
			}
		}
	})

	if foundStatus == "" {
		text := strings.ToLower(doc.Text())
		if strings.Contains(text, "tidak ada") {
			return "Tidak Ada", nil
		}
		if strings.Contains(text, "ada") {
			return "Ada", nil
		}
		return "", fmt.Errorf("status tidak ketemu di HTML")
	}
	return foundStatus, nil
}

// normalizeDomainStr dipakai internal parser HTML (berbeda dari cleanDomain)
func normalizeDomainStr(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "www.")
	s = strings.TrimSuffix(s, "/")
	return s
}

// trustPositifToBotStatus konversi hasil "Ada"/"Tidak Ada" ke "BLOCKED"/"SAFE"
func trustPositifToBotStatus(status string) string {
	s := strings.ToLower(strings.TrimSpace(status))
	if strings.Contains(s, "tidak ada") {
		return "SAFE"
	}
	if strings.Contains(s, "ada") {
		return "BLOCKED"
	}
	return "ERROR"
}
