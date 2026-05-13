package bot

import (
	"fmt"
	"log"
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

// handleAddPrompt dipanggil saat klik tombol ➕ Add Domain
func (h *Handler) handleAddPrompt(c telebot.Context) error {
	userID := c.Sender().ID
	chatID := c.Chat().ID

	h.wizard.set(userID, &wizardSession{
		Step:   stepWaitDomain,
		ChatID: chatID,
	})

	return c.Send(
		"➕ *Tambah Domain*\n\n"+
			"Kirim domain yang ingin ditambahkan:\n"+
			"_(contoh: contoh.com)_",
		cancelInlineMarkup(), telebot.ModeMarkdown,
	)
}

// handleCheckPrompt dipanggil saat klik tombol 🔍 Check Domain
func (h *Handler) handleCheckPrompt(c telebot.Context) error {
	userID := c.Sender().ID
	chatID := c.Chat().ID

	h.wizard.set(userID, &wizardSession{
		Step:   stepCheckDomain,
		ChatID: chatID,
	})

	return c.Send(
		"🔍 *Cek Domain*\n\n"+
			"Kirim domain yang ingin dicek:\n"+
			"_(contoh: google.com)_",
		cancelInlineMarkup(), telebot.ModeMarkdown,
	)
}

// handleRemovePrompt dipanggil saat klik tombol 🗑️ Remove Domain
func (h *Handler) handleRemovePrompt(c telebot.Context) error {
	userID := c.Sender().ID
	chatID := c.Chat().ID

	h.wizard.set(userID, &wizardSession{
		Step:   stepRemoveDomain,
		ChatID: chatID,
	})

	return c.Send(
		"🗑️ *Hapus Domain*\n\n"+
			"Kirim domain yang ingin dihapus:\n"+
			"_(contoh: google.com)_",
		cancelInlineMarkup(), telebot.ModeMarkdown,
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
			return c.Send("❌ Domain tidak valid, coba lagi:", cancelInlineMarkup())
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
					"📂 *Pilih kategori baru atau ketik nama kategori:*",
				domain, existingCategory)
		} else {
			msg = fmt.Sprintf("✅ Domain: `%s`\n\n📂 *Pilih kategori atau ketik nama baru:*", domain)
		}
		return c.Send(msg, h.buildCategoryInlineMenu(sess.ChatID), telebot.ModeMarkdown)

	case stepWaitLabel:
		label := strings.ToUpper(strings.TrimSpace(text))
		if label == "" {
			return c.Send("❌ Kategori tidak boleh kosong, coba lagi:", cancelInlineMarkup())
		}
		return h.doAddDomain(c, sess.Domain, label)

	case stepCheckDomain:
		h.wizard.delete(userID)
		domain := store.CleanDomain(text)
		if domain == "" {
			return c.Send("❌ Domain tidak valid", telebot.ModeMarkdown)
		}
		return h.doCheckDomain(c, domain)

	case stepRemoveDomain:
		h.wizard.delete(userID)
		domain := store.CleanDomain(text)
		if domain == "" {
			return c.Send("❌ Domain tidak valid", telebot.ModeMarkdown)
		}
		return h.doRemoveDomain(c, domain)
	}

	return nil
}

// ==================== DOMAIN OPERATIONS ====================

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
			"⚠️ Domain `%s` tidak ditemukan di list", domain), telebot.ModeMarkdown)
	}

	if err := h.ch.SaveURLs(chatID, urlsByLabel); err != nil {
		return c.Send("❌ Gagal menyimpan perubahan")
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

	return c.Send(msg, telebot.ModeMarkdown)
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
					return c.Send(fmt.Sprintf(
						"⚠️ Domain `%s` sudah ada di kategori *%s*",
						domain, cat), telebot.ModeMarkdown)
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

	sentMsg, _ := c.Bot().Send(c.Chat(), confirmMsg, telebot.ModeMarkdown)

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

// ==================== WIZARD CALLBACKS ====================

func (h *Handler) registerWizardCallbacks() {
	// Batal wizard via inline button
	h.bot.Handle("\fwizard_cancel", func(c telebot.Context) error {
		h.wizard.delete(c.Sender().ID)
		c.Respond(&telebot.CallbackResponse{Text: "Dibatalkan"})
		return c.Edit("❌ *Dibatalkan.*", telebot.ModeMarkdown)
	})
}
