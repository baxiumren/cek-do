package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"myapp/config"
)

const stickyBlockedFile = "sticky_blocked.json"

// Store holds all persistent/in-memory state
type Store struct {
	stickyBlocked map[string]time.Time
	stickyMu      sync.RWMutex

	forceBlocked map[string]string
	forceMu      sync.RWMutex
}

func New() *Store {
	return &Store{
		stickyBlocked: make(map[string]time.Time),
		forceBlocked:  make(map[string]string),
	}
}

// ==================== STICKY BLOCK ====================

func (s *Store) LoadStickyBlocked() {
	s.stickyMu.Lock()
	defer s.stickyMu.Unlock()

	data, err := os.ReadFile(stickyBlockedFile)
	if err != nil {
		log.Printf("[STICKY] No existing file, starting fresh")
		return
	}
	var loaded map[string]string
	if err := json.Unmarshal(data, &loaded); err != nil {
		log.Printf("[STICKY] Error parsing file: %v", err)
		return
	}
	for domain, timeStr := range loaded {
		t, _ := time.Parse(time.RFC3339, timeStr)
		s.stickyBlocked[domain] = t
	}
	log.Printf("[STICKY] Loaded %d domains from file", len(s.stickyBlocked))
}

func (s *Store) saveStickyBlocked() {
	s.stickyMu.RLock()
	defer s.stickyMu.RUnlock()

	toSave := make(map[string]string)
	for domain, t := range s.stickyBlocked {
		toSave[domain] = t.Format(time.RFC3339)
	}
	data, err := json.MarshalIndent(toSave, "", "  ")
	if err != nil {
		log.Printf("[STICKY] Error marshaling: %v", err)
		return
	}
	if err := os.WriteFile(stickyBlockedFile, data, 0644); err != nil {
		log.Printf("[STICKY] Error saving file: %v", err)
	}
}

func (s *Store) IsStickyBlocked(domain string) (bool, time.Time) {
	s.stickyMu.RLock()
	defer s.stickyMu.RUnlock()
	t, exists := s.stickyBlocked[domain]
	return exists, t
}

func (s *Store) AddStickyBlocked(domain string) {
	s.stickyMu.Lock()
	if _, exists := s.stickyBlocked[domain]; !exists {
		s.stickyBlocked[domain] = time.Now()
		log.Printf("[STICKY] Added: %s", domain)
		s.stickyMu.Unlock()
		go s.saveStickyBlocked()
		return
	}
	s.stickyMu.Unlock()
}

func (s *Store) RemoveStickyBlocked(domain string) bool {
	s.stickyMu.Lock()
	if _, exists := s.stickyBlocked[domain]; exists {
		delete(s.stickyBlocked, domain)
		s.stickyMu.Unlock()
		go s.saveStickyBlocked()
		return true
	}
	s.stickyMu.Unlock()
	return false
}

func (s *Store) GetStickyList() map[string]time.Time {
	s.stickyMu.RLock()
	defer s.stickyMu.RUnlock()
	result := make(map[string]time.Time)
	for k, v := range s.stickyBlocked {
		result[k] = v
	}
	return result
}

// ==================== FORCE BLOCK ====================

func (s *Store) IsForceBlocked(domain string) bool {
	s.forceMu.RLock()
	defer s.forceMu.RUnlock()
	_, exists := s.forceBlocked[domain]
	return exists
}

func (s *Store) AddForceBlock(domain, label string) {
	s.forceMu.Lock()
	defer s.forceMu.Unlock()
	s.forceBlocked[domain] = label
}

func (s *Store) RemoveForceBlock(domain string) bool {
	s.forceMu.Lock()
	defer s.forceMu.Unlock()
	if _, exists := s.forceBlocked[domain]; exists {
		delete(s.forceBlocked, domain)
		return true
	}
	return false
}

func (s *Store) GetForceList() map[string]string {
	s.forceMu.RLock()
	defer s.forceMu.RUnlock()
	result := make(map[string]string)
	for k, v := range s.forceBlocked {
		result[k] = v
	}
	return result
}

// ==================== URL FILE OPERATIONS ====================

// LoadURLs membaca file domain
// Format file: KATEGORI|domain.com
func (s *Store) LoadURLs(cfg *config.GroupConfig, urlsMu *sync.RWMutex) map[string][]string {
	urlsMu.RLock()
	defer urlsMu.RUnlock()

	urlsByLabel := make(map[string][]string)

	file, err := os.Open(cfg.URLsFile)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[LOAD ERROR] file=%s: %v", cfg.URLsFile, err)
		}
		return urlsByLabel
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		idx := strings.Index(line, "|")
		if idx == -1 {
			log.Printf("[LOAD WARN] file=%s line %d: format salah (tidak ada '|'): %q", cfg.URLsFile, lineNum, line)
			continue
		}

		label := strings.TrimSpace(strings.ToUpper(line[:idx]))
		domain := CleanDomain(line[idx+1:])

		if label == "" || domain == "" {
			log.Printf("[LOAD WARN] file=%s line %d: label atau domain kosong", cfg.URLsFile, lineNum)
			continue
		}

		urlsByLabel[label] = append(urlsByLabel[label], domain)
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[LOAD ERROR] file=%s scanner: %v", cfg.URLsFile, err)
	}

	// Deduplicate dan sort
	for label, domains := range urlsByLabel {
		seen := make(map[string]bool)
		var clean []string
		for _, d := range domains {
			if !seen[d] {
				seen[d] = true
				clean = append(clean, d)
			}
		}
		sort.Strings(clean)
		urlsByLabel[label] = clean
	}

	log.Printf("[LOAD] file=%s → %d kategori, total domain=%d",
		cfg.URLsFile, len(urlsByLabel), CountDomains(urlsByLabel))

	return urlsByLabel
}

// SaveURLs menyimpan domain ke file
func (s *Store) SaveURLs(cfg *config.GroupConfig, urlsByLabel map[string][]string, urlsMu *sync.RWMutex) error {
	urlsMu.Lock()
	defer urlsMu.Unlock()

	tmpFile := cfg.URLsFile + ".tmp"

	file, err := os.Create(tmpFile)
	if err != nil {
		return fmt.Errorf("failed to create temp file %s: %v", tmpFile, err)
	}

	var labels []string
	for label := range urlsByLabel {
		labels = append(labels, label)
	}
	sort.Strings(labels)

	writer := bufio.NewWriter(file)
	totalWritten := 0

	for _, label := range labels {
		for _, domain := range urlsByLabel[label] {
			if domain == "" {
				continue
			}
			fmt.Fprintf(writer, "%s|%s\n", label, domain)
			totalWritten++
		}
	}

	if err := writer.Flush(); err != nil {
		file.Close()
		os.Remove(tmpFile)
		return fmt.Errorf("failed to flush: %v", err)
	}
	file.Close()

	if err := os.Rename(tmpFile, cfg.URLsFile); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("failed to rename: %v", err)
	}

	log.Printf("[SAVE] file=%s → %d domain saved", cfg.URLsFile, totalWritten)
	return nil
}

// ==================== HELPERS ====================

func CountDomains(urlsByLabel map[string][]string) int {
	total := 0
	for _, domains := range urlsByLabel {
		total += len(domains)
	}
	return total
}

func CleanDomain(input string) string {
	d := strings.ToLower(strings.TrimSpace(input))
	d = strings.TrimPrefix(d, "http://")
	d = strings.TrimPrefix(d, "https://")
	d = strings.TrimPrefix(d, "www.")
	d = strings.TrimSuffix(d, "/")
	return d
}
