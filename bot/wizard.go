package bot

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"myapp/checker"
	"myapp/store"
	"gopkg.in/telebot.v3"
)

// ==================== WIZARD SESSION ====================

type wizardStep int

const (
	stepWaitDomain wizardStep = iota
	stepWaitLabel
	stepCheckDomain
	stepRemoveDomain
	stepListCategory
	stepSetInterval
)

type wizardSession struct {
	Step   wizardStep
	Domain string
	ChatID int64
}

type wizardStore struct {
	mu       sync.Mutex
	sessions map[int64]*wizardSession // key: userID
}

func newWizardStore() *wizardStore {
	return &wizardStore{
		sessions: make(map[int64]*wizardSession),
	}
}

func (ws *wizardStore) set(userID int64, sess *wizardSession) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.sessions[userID] = sess
}

func (ws *wizardStore) get(userID int64) (*wizardSession, bool) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	sess, ok := ws.sessions[userID]
	return sess, ok
}

func (ws *wizardStore) delete(userID int64) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	delete(ws.sessions, userID)
}

// ==================== WIZARD HANDLERS ====================

// handleAddPrompt dipanggil saat klik tombol "➕ Add Domain"
func (h *Handler) handleAddPrompt(c telebot.Context) error {
	userID := c.Sender().ID
	chatID := c.Chat().ID

	h.wizard.set(userID, &wizardSession{
		Step:   stepWaitDomain,
		ChatID: chatID,
	})

	// Ganti keyboard bawah → hanya tombol Batal
	return c.Send(
		"➕ *Tambah Domain*\n\n"+
			"Kirim domain yang ingin ditambahkan:\n"+
			"_(contoh: contoh.com)_",
		cancelReplyKeyboard(), telebot.ModeMarkdown,
	)
}

// handleWizardStep dipanggil dari OnText ketika user sedang dalam wizard
func (h *Handler) handleWizardStep(c telebot.Context, sess *wizardSession) error {
	userID := c.Sender().ID
	text := strings.TrimSpace(c.Text())

	switch sess.Step {

	case stepWaitDomain:
		domain := store.CleanDomain(text)
		if domain == "" {
			return c.Send("❌ Domain tidak valid, coba lagi:", cancelReplyKeyboard())
		}

		// Cek apakah domain sudah terdaftar di list
		urlsByLabel := h.ch.LoadURLs(sess.ChatID)
		var existingCategory string
		for label, domains := range urlsByLabel {
			for _, d := range domains {
				if d == domain {
					existingCategory = label
					break
				}
			}
			if existingCategory != "" {
				break
			}
		}

		// Simpan domain, minta label
		sess.Domain = domain
		sess.Step = stepWaitLabel
		h.wizard.set(userID, sess)

		var msg string
		if existingCategory != "" {
			msg = fmt.Sprintf(
				"⚠️ Domain `%s` sudah terdaftar di kategori *%s*\n\n"+
					"📂 *Pilih kategori baru untuk memindahkan:*\n"+
					"_(atau tekan ❌ Batal untuk membatalkan)_",
				domain, existingCategory)
		} else {
			msg = fmt.Sprintf("✅ Domain: `%s`\n\n📂 *Ketik atau pilih kategori:*", domain)
		}
		return c.Send(msg, h.labelReplyKeyboard(sess.ChatID), telebot.ModeMarkdown)

	case stepWaitLabel:
		label := strings.ToUpper(strings.TrimSpace(text))
		if label == "" {
			return c.Send("❌ Kategori tidak boleh kosong, coba lagi:", cancelReplyKeyboard())
		}
		return h.doAddDomain(c, sess.Domain, label)

	case stepCheckDomain:
		h.wizard.delete(userID)
		domain := store.CleanDomain(text)
		if domain == "" {
			return c.Send("❌ Domain tidak valid", h.replyKeyboard(), telebot.ModeMarkdown)
		}
		return h.doCheckDomain(c, domain)

	case stepRemoveDomain:
		h.wizard.delete(userID)
		domain := store.CleanDomain(text)
		if domain == "" {
			return c.Send("❌ Domain tidak valid", h.replyKeyboard(), telebot.ModeMarkdown)
		}
		return h.doRemoveDomain(c, domain)

	case stepListCategory:
		h.wizard.delete(userID)
		filter := ""
		if text != "📋 Semua Domain" {
			filter = strings.ToUpper(strings.TrimSpace(text))
		}
		return h.showList(c, filter)

	case stepSetInterval:
		h.wizard.delete(userID)
		d := parseIntervalText(text)
		if d == 0 {
			return c.Send("❌ Pilihan tidak valid", h.replyKeyboard())
		}
		h.ch.SetCheckInterval(sess.ChatID, d)
		return c.Send(
			fmt.Sprintf("✅ *Interval cek diubah!*\n\n⏱️ Domain akan dicek setiap *%s*", formatIntervalDuration(d)),
			h.replyKeyboard(), telebot.ModeMarkdown,
		)
	}

	return nil
}

// ==================== INTERVAL HELPERS ====================

func intervalReplyKeyboard() *telebot.ReplyMarkup {
	menu := &telebot.ReplyMarkup{ResizeKeyboard: true}
	menu.Reply(
		menu.Row(menu.Text("30 Detik"), menu.Text("45 Detik"), menu.Text("1 Menit")),
		menu.Row(menu.Text("2 Menit"), menu.Text("5 Menit"), menu.Text("10 Menit")),
		menu.Row(menu.Text("15 Menit"), menu.Text("30 Menit")),
		menu.Row(menu.Text("❌ Batal")),
	)
	return menu
}

func parseIntervalText(text string) time.Duration {
	switch text {
	case "30 Detik":
		return 30 * time.Second
	case "45 Detik":
		return 45 * time.Second
	case "1 Menit":
		return 1 * time.Minute
	case "2 Menit":
		return 2 * time.Minute
	case "5 Menit":
		return 5 * time.Minute
	case "10 Menit":
		return 10 * time.Minute
	case "15 Menit":
		return 15 * time.Minute
	case "30 Menit":
		return 30 * time.Minute
	}
	return 0
}

func formatIntervalDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.0f detik", d.Seconds())
	}
	if d == time.Minute {
		return "1 menit"
	}
	return fmt.Sprintf("%.0f menit", d.Minutes())
}

// doRemoveDomain proses hapus domain dari list
func (h *Handler) doRemoveDomain(c telebot.Context, domain string) error {
	chatID := c.Chat().ID
	urlsByLabel := h.ch.LoadURLs(chatID)

	found := false
	var domainLabel string
	for label, domains := range urlsByLabel {
		for i, d := range domains {
			if d == domain {
				domainLabel = label
				urlsByLabel[label] = append(domains[:i], domains[i+1:]...)
				if len(urlsByLabel[label]) == 0 {
					delete(urlsByLabel, label)
				}
				found = true
				break
			}
		}
		if found {
			break
		}
	}

	if !found {
		return c.Send(fmt.Sprintf(
			"⚠️ Domain `%s` tidak ditemukan di list",
			domain), h.replyKeyboard(), telebot.ModeMarkdown)
	}

	if err := h.ch.SaveURLs(chatID, urlsByLabel); err != nil {
		return c.Send("❌ Gagal menyimpan perubahan", h.replyKeyboard())
	}

	state := h.ch.GetGroupState(chatID)
	wasBlocked := false
	if state != nil {
		state.BlockedMu.Lock()
		if _, exists := state.BlockedDomains[domain]; exists {
			wasBlocked = true
			delete(state.BlockedDomains, domain)
		}
		state.BlockedMu.Unlock()
	}
	h.st.RemoveStickyBlocked(domain)

	msg := fmt.Sprintf(
		"🗑️ *Domain dihapus!*\n\n🌐 Domain: `%s`\n📂 Kategori: *%s*",
		domain, domainLabel)
	if wasBlocked {
		msg += "\n\n⚠️ Domain ini sebelumnya terblokir — alert cycle dihentikan"
	}

	return c.Send(msg, h.replyKeyboard(), telebot.ModeMarkdown)
}

// doAddDomain proses simpan domain ke file (dipakai wizard text + callback tombol kategori)
func (h *Handler) doAddDomain(c telebot.Context, domain, label string) error {
	chatID := c.Chat().ID
	h.wizard.delete(c.Sender().ID)

	urlsByLabel := h.ch.LoadURLs(chatID)

	var oldCategory string
	isEdit := false
	for cat, domains := range urlsByLabel {
		for i, d := range domains {
			if d == domain {
				if cat == label {
					h.wizard.delete(c.Sender().ID)
					return c.Send(fmt.Sprintf(
						"⚠️ Domain `%s` sudah ada di kategori *%s*",
						domain, cat), h.replyKeyboard(), telebot.ModeMarkdown)
				}
				oldCategory = cat
				isEdit = true
				urlsByLabel[cat] = append(domains[:i], domains[i+1:]...)
				if len(urlsByLabel[cat]) == 0 {
					delete(urlsByLabel, cat)
				}
				break
			}
		}
		if isEdit {
			break
		}
	}

	urlsByLabel[label] = append(urlsByLabel[label], domain)

	if err := h.ch.SaveURLs(chatID, urlsByLabel); err != nil {
		log.Printf("[WIZARD ADD ERROR] chat=%d: %v", chatID, err)
		return c.Send("❌ Gagal menyimpan domain")
	}

	log.Printf("[WIZARD ADD] chat=%d domain=%s label=%s isEdit=%v", chatID, domain, label, isEdit)

	// Balas sukses langsung tanpa nunggu cek API
	var confirmMsg string
	if isEdit {
		confirmMsg = fmt.Sprintf(
			"✏️ *Domain dipindahkan!*\n\n🌐 Domain: `%s`\n📂 Dari: *%s* → Ke: *%s*\n\n"+
				"📡 Mengecek status ke KOMINFO...",
			domain, oldCategory, label)
	} else {
		confirmMsg = fmt.Sprintf(
			"✅ *Domain berhasil ditambahkan!*\n\n🌐 Domain: `%s`\n📂 Kategori: *%s*\n\n"+
				"📡 Mengecek status ke KOMINFO...",
			domain, label)
	}

	sentMsg, _ := c.Bot().Send(c.Chat(), confirmMsg, h.replyKeyboard(), telebot.ModeMarkdown)

	// Priority check di background — update pesan setelah selesai
	go func() {
		status := h.ch.CheckDomainFast(domain)

		var statusLine string
		switch status {
		case "BLOCKED":
			statusLine = "🛑 Status: *DIBLOKIR KOMINFO*\n🚨 Alert cycle akan dimulai"
			state := h.ch.GetGroupState(chatID)
			if state != nil {
				state.BlockedMu.Lock()
				if _, exists := state.BlockedDomains[domain]; !exists {
					state.BlockedDomains[domain] = &checker.DomainAlertCycle{
						LastAlertTime:   time.Now(),
						CycleStartTime:  time.Now(),
						AlertCount:      0,
						InCooldown:      false,
						LastCycleNumber: 1,
					}
				}
				state.BlockedMu.Unlock()
			}
		case "SAFE":
			statusLine = "🟢 Status: *AMAN*"
		default:
			statusLine = "⚠️ Gagal cek status API"
		}

		// Edit pesan konfirmasi dengan hasil cek
		finalMsg := strings.Replace(confirmMsg, "\n\n📡 Mengecek status ke KOMINFO...", "\n\n"+statusLine, 1)
		if sentMsg != nil {
			h.bot.Edit(sentMsg, finalMsg, telebot.ModeMarkdown)
		}
	}()

	return nil
}

// ==================== WIZARD MARKUP HELPERS ====================

// cancelReplyKeyboard — keyboard bawah saat wizard aktif, hanya ada tombol Batal
func cancelReplyKeyboard() *telebot.ReplyMarkup {
	menu := &telebot.ReplyMarkup{ResizeKeyboard: true}
	menu.Reply(menu.Row(menu.Text("❌ Batal")))
	return menu
}

// labelReplyKeyboard — tombol pilih kategori di keyboard bawah
func (h *Handler) labelReplyKeyboard(chatID int64) *telebot.ReplyMarkup {
	urlsByLabel := h.ch.LoadURLs(chatID)

	var labels []string
	for label := range urlsByLabel {
		labels = append(labels, label)
	}
	sort.Strings(labels)

	menu := &telebot.ReplyMarkup{ResizeKeyboard: true}
	var rows []telebot.Row

	// 3 tombol per baris
	for i := 0; i < len(labels); i += 3 {
		var btns []telebot.Btn
		for j := i; j < i+3 && j < len(labels); j++ {
			btns = append(btns, menu.Text(labels[j]))
		}
		rows = append(rows, menu.Row(btns...))
	}

	// Batal selalu di baris paling bawah
	rows = append(rows, menu.Row(menu.Text("❌ Batal")))
	menu.Reply(rows...)
	return menu
}

// listCategoryReplyKeyboard — tombol pilih kategori untuk fitur List Domain
func (h *Handler) listCategoryReplyKeyboard(chatID int64) *telebot.ReplyMarkup {
	urlsByLabel := h.ch.LoadURLs(chatID)

	var labels []string
	for label := range urlsByLabel {
		labels = append(labels, label)
	}
	sort.Strings(labels)

	menu := &telebot.ReplyMarkup{ResizeKeyboard: true}
	var rows []telebot.Row

	// Semua Domain di baris paling atas
	rows = append(rows, menu.Row(menu.Text("📋 Semua Domain")))

	// Tombol per kategori, 3 per baris
	for i := 0; i < len(labels); i += 3 {
		var btns []telebot.Btn
		for j := i; j < i+3 && j < len(labels); j++ {
			btns = append(btns, menu.Text(labels[j]))
		}
		rows = append(rows, menu.Row(btns...))
	}

	// Batal di baris paling bawah
	rows = append(rows, menu.Row(menu.Text("❌ Batal")))
	menu.Reply(rows...)
	return menu
}

// ==================== WIZARD CALLBACKS ====================

func (h *Handler) registerWizardCallbacks() {
	// Batal wizard via inline button (dari step label)
	h.bot.Handle("\fwizard_cancel", func(c telebot.Context) error {
		h.wizard.delete(c.Sender().ID)
		c.Respond(&telebot.CallbackResponse{Text: "Dibatalkan"})
		c.Delete()
		h.bot.Send(c.Chat(), "❌ *Dibatalkan.*", h.replyKeyboard(), telebot.ModeMarkdown)
		return nil
	})

}
