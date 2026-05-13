package checker

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"myapp/config"
	"myapp/store"
)

// ==================== CONSTANTS ====================

const (
	MaxViolations     = 3
	TimeWindow        = time.Hour
	SpamInterval      = 25 * time.Second
	CheckInterval     = 45 * time.Second
	APITimeout        = 15 * time.Second
	MaxRetries        = 2
	AlertDuration     = 2 * time.Minute
	CooldownDuration  = 10 * time.Minute
	MaxAlertsPerCycle = 8

	MaxConcurrentChecks = 10
	DelayPerDomain      = 200 * time.Millisecond
)

// ==================== STRUCTURES ====================

type DomainEntry struct {
	Domain string
	Label  string
}

type UserViolation struct {
	Timestamps []time.Time
	Mu         sync.RWMutex
}

type DomainAlertCycle struct {
	LastAlertTime   time.Time
	CycleStartTime  time.Time
	AlertCount      int
	InCooldown      bool
	LastCycleNumber int
}

type GroupState struct {
	BlockedDomains      map[string]*DomainAlertCycle
	UserViolations      map[int64]*UserViolation
	AdminWhitelist      map[int64]bool
	URLsMu              sync.RWMutex
	BlockedMu           sync.RWMutex
	ViolationMu         sync.RWMutex
	CustomCheckInterval time.Duration // 0 = pakai default CheckInterval
	IntervalMu          sync.RWMutex
}

type CheckResult struct {
	Domain string
	Label  string
	Status string
}

// BotSender interface untuk mengirim pesan ke Telegram (memutus circular dependency)
type BotSender interface {
	SendToChat(chatID int64, msg string) error
	// SendBlockAlert kirim alert block dengan tombol aksi (domain dipakai buat bikin keyboard)
	SendBlockAlert(chatID int64, msg string, domain string) error
}

// ==================== CHECKER ====================

type Checker struct {
	cfg         *config.Config
	st          *store.Store
	bot         BotSender
	groupStates map[int64]*GroupState
	httpClient  *http.Client
}

func New(cfg *config.Config, st *store.Store, bot BotSender) *Checker {
	return &Checker{
		cfg: cfg,
		st:  st,
		bot: bot,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// SetGroupStates injects the group states map (set from main after building states)
func (ch *Checker) SetGroupStates(states map[int64]*GroupState) {
	ch.groupStates = states
}

func (ch *Checker) GetGroupStates() map[int64]*GroupState {
	return ch.groupStates
}

func (ch *Checker) GetGroupState(chatID int64) *GroupState {
	return ch.groupStates[chatID]
}

func (ch *Checker) GetCheckInterval(chatID int64) time.Duration {
	state := ch.groupStates[chatID]
	if state == nil {
		return CheckInterval
	}
	state.IntervalMu.RLock()
	defer state.IntervalMu.RUnlock()
	if state.CustomCheckInterval == 0 {
		return CheckInterval
	}
	return state.CustomCheckInterval
}

func (ch *Checker) SetCheckInterval(chatID int64, d time.Duration) {
	state := ch.groupStates[chatID]
	if state == nil {
		return
	}
	state.IntervalMu.Lock()
	state.CustomCheckInterval = d
	state.IntervalMu.Unlock()
	log.Printf("[INTERVAL] Grup %d: interval diubah ke %v", chatID, d)
}

// GetGroupConfig helper
func (ch *Checker) GetGroupConfig(chatID int64) *config.GroupConfig {
	for i := range ch.cfg.Groups {
		if ch.cfg.Groups[i].GroupID == chatID {
			return &ch.cfg.Groups[i]
		}
	}
	return nil
}

// ==================== DOMAIN CHECK ====================

func (ch *Checker) CheckDomainFast(domain string) string {
	if ch.st.IsForceBlocked(domain) {
		return "BLOCKED"
	}
	if blocked, _ := ch.st.IsStickyBlocked(domain); blocked {
		return "BLOCKED"
	}

	for round := 1; round <= 2; round++ {
		result := ch.doAPICheck(domain, round)
		if result == "BLOCKED" {
			ch.st.AddStickyBlocked(domain)
			return "BLOCKED"
		}
		if round == 1 && result == "SAFE" {
			time.Sleep(100 * time.Millisecond)
		}
	}
	return "SAFE"
}

func (ch *Checker) CheckDomainManual(domain string) (string, int, int) {
	if ch.st.IsForceBlocked(domain) {
		return "BLOCKED", 3, 3
	}
	if blocked, _ := ch.st.IsStickyBlocked(domain); blocked {
		return "BLOCKED", 3, 3
	}

	blockedCount := 0
	totalRounds := 3
	for round := 1; round <= totalRounds; round++ {
		result := ch.doAPICheck(domain, round)
		if result == "BLOCKED" {
			blockedCount++
		}
		if round < totalRounds {
			time.Sleep(200 * time.Millisecond)
		}
	}

	if blockedCount > 0 {
		ch.st.AddStickyBlocked(domain)
		return "BLOCKED", blockedCount, totalRounds
	}
	return "SAFE", 0, totalRounds
}

func (ch *Checker) doAPICheck(domain string, round int) string {
	for attempt := 1; attempt <= MaxRetries; attempt++ {
		status, err := checkTrustPositif(domain)
		if err != nil {
			log.Printf("[TRUSTPOSITIF ERROR] Round %d Attempt %d - Domain %s: %v", round, attempt, domain, err)
			if attempt < MaxRetries {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			return "ERROR"
		}
		result := trustPositifToBotStatus(status)
		log.Printf("[TRUSTPOSITIF] Round %d - Domain %s: %s (%s)", round, domain, result, status)
		return result
	}
	return "ERROR"
}

// ==================== LOAD URLS ====================

func (ch *Checker) LoadURLs(chatID int64) map[string][]string {
	groupConfig := ch.GetGroupConfig(chatID)
	if groupConfig == nil {
		log.Printf("[LOAD] GetGroupConfig nil for chatID=%d", chatID)
		return make(map[string][]string)
	}
	state := ch.groupStates[chatID]
	if state == nil {
		return make(map[string][]string)
	}
	return ch.st.LoadURLs(groupConfig, &state.URLsMu)
}

func (ch *Checker) SaveURLs(chatID int64, urlsByLabel map[string][]string) error {
	groupConfig := ch.GetGroupConfig(chatID)
	if groupConfig == nil {
		return fmt.Errorf("group config not found for chatID=%d", chatID)
	}
	state := ch.groupStates[chatID]
	if state == nil {
		return fmt.Errorf("state not found for chatID=%d", chatID)
	}
	return ch.st.SaveURLs(groupConfig, urlsByLabel, &state.URLsMu)
}

// ==================== AUTO CHECK TASK ====================

func (ch *Checker) StartAutoCheck(chatID int64) {
	time.Sleep(2 * time.Second)
	log.Printf("[AUTO CHECK] Task dimulai untuk grup %d...", chatID)

	state := ch.groupStates[chatID]
	if state == nil {
		return
	}

	for {
		startTime := time.Now()
		urlsByLabel := ch.LoadURLs(chatID)

		var allDomains []DomainEntry
		for label, domains := range urlsByLabel {
			for _, domain := range domains {
				allDomains = append(allDomains, DomainEntry{Domain: domain, Label: label})
			}
		}

		// Baca interval dinamis dari state
		state.IntervalMu.RLock()
		interval := state.CustomCheckInterval
		state.IntervalMu.RUnlock()
		if interval == 0 {
			interval = CheckInterval
		}

		totalDomains := len(allDomains)
		if totalDomains == 0 {
			time.Sleep(interval)
			continue
		}

		log.Printf("[AUTO CHECK] Checking %d domains for group %d...", totalDomains, chatID)

		results := make(chan CheckResult, totalDomains)
		semaphore := make(chan struct{}, MaxConcurrentChecks)
		var wg sync.WaitGroup

		for _, entry := range allDomains {
			wg.Add(1)
			go func(e DomainEntry) {
				defer wg.Done()
				semaphore <- struct{}{}
				defer func() { <-semaphore }()

				status := ch.CheckDomainFast(e.Domain)
				results <- CheckResult{
					Domain: e.Domain,
					Label:  e.Label,
					Status: status,
				}
				time.Sleep(DelayPerDomain)
			}(entry)
		}

		go func() {
			wg.Wait()
			close(results)
		}()

		for result := range results {
			ch.processCheckResult(chatID, state, result)
		}

		elapsed := time.Since(startTime)
		log.Printf("[AUTO CHECK] Completed %d domains in %.1fs for group %d (interval: %v)", totalDomains, elapsed.Seconds(), chatID, interval)

		if elapsed < interval {
			time.Sleep(interval - elapsed)
		}
	}
}

func (ch *Checker) processCheckResult(chatID int64, state *GroupState, result CheckResult) {
	domain := result.Domain
	label := result.Label
	status := result.Status

	if status == "BLOCKED" {
		state.BlockedMu.Lock()
		if cycle, exists := state.BlockedDomains[domain]; !exists {
			cycle = &DomainAlertCycle{
				LastAlertTime:   time.Now(),
				CycleStartTime:  time.Now(),
				AlertCount:      0,
				InCooldown:      false,
				LastCycleNumber: 1,
			}
			state.BlockedDomains[domain] = cycle

			msg := fmt.Sprintf(
				"🚨 *BLOCK ALERT!* [Cycle #1]\n"+
					"📛 Label: `%s`\n"+
					"🌐 Domain: `%s`\n"+
					"📴 Status: *DIBLOKIR KOMINFO*\n\n"+
					"⏰ *Mode:* Alert Aktif (2 menit)\n"+
					"📈 Alert: #1",
				label, domain,
			)
			ch.bot.SendBlockAlert(chatID, msg, domain)
			cycle.AlertCount++
			cycle.LastAlertTime = time.Now()
		} else {
			now := time.Now()
			elapsed := now.Sub(cycle.CycleStartTime)

			if !cycle.InCooldown {
				if elapsed > AlertDuration {
					cycle.InCooldown = true
					cycle.CycleStartTime = now
				}
			} else {
				if elapsed >= CooldownDuration {
					cycle.InCooldown = false
					cycle.CycleStartTime = now
					cycle.AlertCount = 0
					cycle.LastCycleNumber++

					msg := fmt.Sprintf(
						"🔔 *ALERT CYCLE RESTART!* [Cycle #%d]\n"+
							"🌐 Domain: `%s`\n"+
							"📛 Label: `%s`\n\n"+
							"⏰ *Mode:* Alert Aktif (2 menit)\n"+
							"📈 Alert: #1",
						cycle.LastCycleNumber, domain, label,
					)
					ch.bot.SendBlockAlert(chatID, msg, domain)
					cycle.AlertCount++
					cycle.LastAlertTime = now
				}
			}
		}
		state.BlockedMu.Unlock()

	} else if status == "SAFE" {
		state.BlockedMu.Lock()
		if _, exists := state.BlockedDomains[domain]; exists {
			delete(state.BlockedDomains, domain)
			log.Printf("[REMOVED-FROM-BLOCKED] Grup %d: %s (AMAN)", chatID, domain)
		}
		state.BlockedMu.Unlock()
	}
}

// ==================== SPAM TASK ====================

func (ch *Checker) StartSpamTask(chatID int64) {
	time.Sleep(3 * time.Second)
	log.Printf("[SPAM NOTIF] Task dimulai untuk grup %d...", chatID)

	state := ch.groupStates[chatID]
	if state == nil {
		return
	}

	for {
		urlsByLabel := ch.LoadURLs(chatID)

		validDomains := make(map[string]bool)
		domainToLabel := make(map[string]string)
		for label, domains := range urlsByLabel {
			for _, domain := range domains {
				validDomains[domain] = true
				domainToLabel[domain] = label
			}
		}

		state.BlockedMu.Lock()
		now := time.Now()

		for domain, cycle := range state.BlockedDomains {
			if !validDomains[domain] {
				delete(state.BlockedDomains, domain)
				continue
			}

			label := domainToLabel[domain]

			if !cycle.InCooldown &&
				now.Sub(cycle.CycleStartTime) <= AlertDuration &&
				now.Sub(cycle.LastAlertTime) >= SpamInterval &&
				cycle.AlertCount < MaxAlertsPerCycle {

				cycle.AlertCount++

				var msg string
				switch {
				case cycle.AlertCount <= 3:
					msg = fmt.Sprintf(
						"🛑 *MASIH BLOKIR!* [Cycle #%d - %d/%d]\n"+
							"📛 Label: `%s`\n"+
							"🌐 Domain: `%s`\n\n"+
							"⏰ *Mode:* Alert Aktif\n"+
							"⏱️ Time Left: %.0f detik",
						cycle.LastCycleNumber, cycle.AlertCount, MaxAlertsPerCycle,
						label, domain,
						(AlertDuration - now.Sub(cycle.CycleStartTime)).Seconds(),
					)
				case cycle.AlertCount <= 6:
					msg = fmt.Sprintf(
						"🚨 *BLOCK PERSISTEN!* [Cycle #%d - %d/%d]\n"+
							"📛 Label: `%s`\n"+
							"🌐 Domain: `%s`\n\n"+
							"⚠️ Domain masih diblokir KOMINFO",
						cycle.LastCycleNumber, cycle.AlertCount, MaxAlertsPerCycle,
						label, domain,
					)
				default:
					msg = fmt.Sprintf(
						"🔴 *FINAL ALERTS!* [Cycle #%d - %d/%d]\n"+
							"📛 Label: `%s`\n"+
							"🌐 Domain: `%s`\n\n"+
							"⏰ *Periode alert hampir berakhir*\n"+
							"⏱️ Cooldown dimulai dalam: %.0f detik",
						cycle.LastCycleNumber, cycle.AlertCount, MaxAlertsPerCycle,
						label, domain,
						(AlertDuration - now.Sub(cycle.CycleStartTime)).Seconds(),
					)
				}

				ch.bot.SendBlockAlert(chatID, msg, domain)
				cycle.LastAlertTime = now

				if cycle.AlertCount >= MaxAlertsPerCycle {
					cycle.InCooldown = true
					cycle.CycleStartTime = now
				}
			}
		}

		state.BlockedMu.Unlock()
		time.Sleep(2 * time.Second)
	}
}
