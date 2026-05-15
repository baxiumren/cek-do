package bot

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"myapp/checker"
	"myapp/config"
	"myapp/store"
	"gopkg.in/telebot.v3"
)

// ==================== HANDLER STRUCT ====================

type Handler struct {
	cfg    *config.Config
	st     *store.Store
	ch     *checker.Checker
	bot    *telebot.Bot
	wizard *wizardStore
}

// SendToChat implements checker.BotSender interface
func (h *Handler) SendToChat(chatID int64, msg string) error {
	_, err := h.bot.Send(&telebot.Chat{ID: chatID}, msg, telebot.ModeMarkdown)
	return err
}

// SendBlockAlert implements checker.BotSender interface — kirim alert tanpa tombol inline
func (h *Handler) SendBlockAlert(chatID int64, msg string, domain string) error {
	_, err := h.bot.Send(&telebot.Chat{ID: chatID}, msg, telebot.ModeMarkdown)
	return err
}

func New(b *telebot.Bot, cfg *config.Config, st *store.Store, ch *checker.Checker) *Handler {
	return &Handler{
		cfg:    cfg,
		st:     st,
		ch:     ch,
		bot:    b,
		wizard: newWizardStore(),
	}
}

// ==================== HELPER ====================

// editOrSend — edit pesan in-place jika msg tidak nil, fallback kirim pesan baru
func (h *Handler) editOrSend(c telebot.Context, msg *telebot.Message, text string, opts ...interface{}) error {
	if msg != nil {
		args := append([]interface{}{telebot.ModeMarkdown}, opts...)
		if _, err := h.bot.Edit(msg, text, args...); err == nil {
			return nil
		}
	}
	args := append([]interface{}{telebot.ModeMarkdown}, opts...)
	return c.Send(text, args...)
}

// ==================== HANDLER REGISTRATION ====================

func (h *Handler) Register() {
	h.bot.Use(func(next telebot.HandlerFunc) telebot.HandlerFunc {
		return func(c telebot.Context) error {
			if strings.HasPrefix(c.Text(), "/") {
				chatType := "private"
				if c.Chat() != nil {
					chatType = string(c.Chat().Type)
				}
				log.Printf("📩 Command: %s from %s (ID: %d) in chat %d [%s]",
					c.Text(), getUserName(c.Sender()), c.Sender().ID, c.Chat().ID, chatType)
			}
			return next(c)
		}
	})

	h.bot.Handle("/start", h.handleStart)
	h.bot.Handle("/menu", h.handleMenu)
	h.bot.Handle("/help", h.handleHelp)
	h.bot.Handle("/info", h.handleInfo)
	h.bot.Handle("/check", h.handleCheck)
	h.bot.Handle("/add", h.handleAdd)
	h.bot.Handle("/remove", h.handleRemove)
	h.bot.Handle("/list", h.handleList)
	h.bot.Handle("/myid", h.handleMyID)
	h.bot.Handle("/restart", h.handleRestart)
	h.bot.Handle("/cycle", h.handleCycle)
	h.bot.Handle("/blocked", h.handleBlocked)
	h.bot.Handle("/forceblock", h.handleForceBlock)
	h.bot.Handle("/unforceblock", h.handleUnforceBlock)
	h.bot.Handle("/forcelist", h.handleForceList)
	h.bot.Handle("/resetblock", h.handleResetBlock)
	h.bot.Handle("/stickylist", h.handleStickyList)

	h.bot.Handle(telebot.OnText, func(c telebot.Context) error {
		if strings.HasPrefix(c.Text(), "/") {
			// Cancel wizard kalau user kirim command lain
			h.wizard.delete(c.Sender().ID)
			return nil
		}

		// Kalau sedang dalam wizard, teruskan ke wizard handler
		if sess, ok := h.wizard.get(c.Sender().ID); ok {
			return h.handleWizardStep(c, sess)
		}

		return h.handleMessage(c)
	})

	h.registerCallbacks()
	h.registerWizardCallbacks()
}

// ==================== HANDLERS ====================

func (h *Handler) handleStart(c telebot.Context) error {
	return c.Send(
		"🤖 *Domain Checker Bot - KOMINFO*\n\n"+
			"Halo! Bot monitoring status domain KOMINFO.\n\n"+
			"🔧 *Fitur Utama:*\n"+
			"• Auto cek domain berkala\n"+
			"• Parallel processing (10 domain sekaligus)\n"+
			"• Alert cycle 2m aktif → 10m jeda\n"+
			"• Sticky block (sekali blocked = permanen)\n"+
			"• Multi-grup support\n\n"+
			"Pilih aksi dari menu di bawah:",
		mainMenuMarkup(), telebot.ModeMarkdown,
	)
}

func (h *Handler) handleMenu(c telebot.Context) error {
	return c.Send("🏠 *Menu Utama*\n\nPilih aksi:", mainMenuMarkup(), telebot.ModeMarkdown)
}

func (h *Handler) handleHelp(c telebot.Context) error {
	msg := `🤖 *Domain Checker Bot - KOMINFO*

*Perintah:*
/check <domain> - Cek status domain (3x API)
/add <domain> <kategori> - Tambah domain
/remove <domain> - Hapus domain
/list [kategori] - Lihat daftar domain
/blocked - Lihat domain terblokir
/cycle - Monitor alert cycle
/info - Statistik bot
/myid - Lihat ID kamu
/menu - Tampilkan menu utama

*Sticky Block (Admin):*
/stickylist - Domain pernah blocked
/resetblock <domain> - Reset status blocked

*Force Block (Admin):*
/forceblock <domain> [kategori] - Paksa block
/unforceblock <domain> - Hapus force block
/forcelist - Lihat force block list

*Admin:*
/restart - Restart bot`

	if err := c.Send(msg, telebot.ModeMarkdown); err != nil {
		return c.Send(strings.ReplaceAll(strings.ReplaceAll(msg, "*", ""), "`", ""))
	}
	return nil
}

func (h *Handler) handleInfo(c telebot.Context) error {
	return c.Send(h.buildInfoMessage(c.Chat().ID), menuMarkup(), telebot.ModeMarkdown)
}

func (h *Handler) handleCheck(c telebot.Context) error {
	chatID := c.Chat().ID
	if h.ch.GetGroupConfig(chatID) == nil {
		return c.Send("❌ Grup ini tidak dikonfigurasi")
	}
	if len(c.Args()) == 0 {
		return h.handleCheckPrompt(c)
	}
	return h.doCheckDomain(c, store.CleanDomain(c.Args()[0]), nil)
}

// doCheckDomain cek domain dan edit pesan in-place jika editMsg tidak nil
func (h *Handler) doCheckDomain(c telebot.Context, domain string, editMsg *telebot.Message) error {
	if domain == "" {
		return h.editOrSend(c, editMsg, "❌ Domain tidak valid", menuMarkup())
	}

	// Tampilkan loading — edit in-place jika editMsg ada, else kirim baru
	loadingText := fmt.Sprintf("⏳ *Mengecek domain* `%s`*...*", domain)
	var loadingMsg *telebot.Message
	if editMsg != nil {
		if _, err := h.bot.Edit(editMsg, loadingText, telebot.ModeMarkdown); err == nil {
			loadingMsg = editMsg
		}
	}
	if loadingMsg == nil {
		loadingMsg, _ = h.bot.Send(c.Chat(), loadingText, telebot.ModeMarkdown)
	}

	isSticky, stickyTime := h.st.IsStickyBlocked(domain)
	status, trueCount, totalCheck := h.ch.CheckDomainManual(domain)

	// Cek apakah domain ada di monitored list
	inList := h.isDomainInList(c.Chat().ID, domain)

	var resultText string
	var inlineMenu *telebot.ReplyMarkup

	switch status {
	case "BLOCKED":
		stickyInfo := ""
		if isSticky {
			stickyInfo = fmt.Sprintf("\n📌 *Sticky:* Sejak %s", stickyTime.Format("02 Jan 2006 15:04"))
		}
		resultText = fmt.Sprintf(
			"🛑 *DIBLOKIR KOMINFO*\n"+
				"🌐 Domain: `%s`\n\n"+
				"⚠️ *Status:* TERBLOKIR\n"+
				"🔍 *API Check:* %d/%d blocked%s\n"+
				"💡 *Saran:* Gunakan domain baru",
			domain, trueCount, totalCheck, stickyInfo)
		if inList {
			inlineMenu = checkResultWithMenuMarkup(domain)
		} else {
			inlineMenu = menuMarkup()
		}
	case "SAFE":
		resultText = fmt.Sprintf(
			"🟢 *AMAN*\n"+
				"🌐 Domain: `%s`\n\n"+
				"✅ Tidak terdaftar dalam Daftar Blokir KOMINFO\n"+
				"🔍 *API Check:* 0/%d blocked",
			domain, totalCheck)
		inlineMenu = menuMarkup()
	default:
		resultText = fmt.Sprintf("⚠️ Gagal cek status: `%s`", domain)
		inlineMenu = menuMarkup()
	}

	// Edit loading message → result
	if loadingMsg != nil {
		if _, err := h.bot.Edit(loadingMsg, resultText, inlineMenu, telebot.ModeMarkdown); err == nil {
			return nil
		}
		// Edit gagal — kalau loading adalah pesan baru (bukan editMsg), hapus dulu
		if editMsg == nil {
			h.bot.Delete(loadingMsg)
		}
	}

	// Fallback: kirim pesan baru
	return c.Send(resultText, inlineMenu, telebot.ModeMarkdown)
}

// isDomainInList cek apakah domain terdaftar di monitored list grup
func (h *Handler) isDomainInList(chatID int64, domain string) bool {
	urlsByLabel := h.ch.LoadURLs(chatID)
	for _, domains := range urlsByLabel {
		for _, d := range domains {
			if d == domain {
				return true
			}
		}
	}
	return false
}

func (h *Handler) handleAdd(c telebot.Context) error {
	chatID := c.Chat().ID
	if h.ch.GetGroupConfig(chatID) == nil {
		return c.Send("❌ Grup ini tidak dikonfigurasi")
	}
	if len(c.Args()) < 2 {
		return c.Send("❌ Contoh: `/add domain.com KATEGORI`", telebot.ModeMarkdown)
	}

	domain := store.CleanDomain(c.Args()[0])
	newCategory := strings.ToUpper(strings.TrimSpace(c.Args()[1]))

	if domain == "" || newCategory == "" {
		return c.Send("❌ Domain atau kategori tidak boleh kosong", telebot.ModeMarkdown)
	}

	urlsByLabel := h.ch.LoadURLs(chatID)

	var oldCategory string
	isEdit := false

	for cat, domains := range urlsByLabel {
		for i, d := range domains {
			if d == domain {
				if cat == newCategory {
					return c.Send(fmt.Sprintf(
						"⚠️ Domain `%s` sudah ada di kategori `%s`",
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

	urlsByLabel[newCategory] = append(urlsByLabel[newCategory], domain)

	if err := h.ch.SaveURLs(chatID, urlsByLabel); err != nil {
		log.Printf("[ADD ERROR] Failed to save for chat %d: %v", chatID, err)
		return c.Send("❌ Gagal menyimpan domain ke file")
	}

	log.Printf("[ADD] chat=%d domain=%s category=%s isEdit=%v", chatID, domain, newCategory, isEdit)

	status := h.ch.CheckDomainFast(domain)

	var msg string
	if isEdit {
		msg = fmt.Sprintf("✏️ Domain `%s` dipindahkan\n📂 Dari: *%s* → Ke: *%s*\n", domain, oldCategory, newCategory)
	} else {
		msg = fmt.Sprintf("✅ Domain `%s` ditambahkan ke *%s*\n", domain, newCategory)
	}

	switch status {
	case "BLOCKED":
		msg += "🛑 Status: *DIBLOKIR KOMINFO*\n"
		msg += "🚨 Alert cycle akan dimulai"
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
		msg += "🟢 Status: *AMAN*"
	default:
		msg += "⚠️ Gagal cek status API"
	}

	return c.Send(msg, telebot.ModeMarkdown)
}

func (h *Handler) handleRemove(c telebot.Context) error {
	chatID := c.Chat().ID
	if len(c.Args()) == 0 {
		return c.Send("❌ Contoh: `/remove domain.com`", telebot.ModeMarkdown)
	}

	domain := store.CleanDomain(c.Args()[0])
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
		return c.Send(fmt.Sprintf("⚠️ Domain `%s` tidak ditemukan di list", domain), telebot.ModeMarkdown)
	}

	if err := h.ch.SaveURLs(chatID, urlsByLabel); err != nil {
		return c.Send("❌ Gagal menyimpan perubahan")
	}

	state := h.ch.GetGroupState(chatID)
	wasBlocked := false
	if state != nil {
		state.BlockedMu.Lock()
		if _, exists := state.BlockedDomains[domain]; exists {
			delete(state.BlockedDomains, domain)
			wasBlocked = true
		}
		state.BlockedMu.Unlock()
	}
	h.st.RemoveStickyBlocked(domain)

	msg := fmt.Sprintf("🗑️ *Domain dihapus!*\n\n🌐 Domain: `%s`\n📂 Kategori: *%s*", domain, domainLabel)
	if wasBlocked {
		msg += "\n\n⚠️ Domain ini terblokir — alert cycle dihentikan"
	}

	return c.Send(msg, telebot.ModeMarkdown)
}

func (h *Handler) handleList(c telebot.Context) error {
	filter := ""
	if len(c.Args()) > 0 {
		filter = strings.ToUpper(c.Args()[0])
	}
	return h.showList(c, filter)
}

// showList — kirim list domain (untuk command /list)
func (h *Handler) showList(c telebot.Context, filterKategori string) error {
	chatID := c.Chat().ID
	urlsByLabel := h.ch.LoadURLs(chatID)

	if len(urlsByLabel) == 0 {
		return c.Send("📭 Belum ada domain yang terdaftar.", telebot.ModeMarkdown)
	}

	var items []checker.DomainEntry
	for label, domains := range urlsByLabel {
		if filterKategori != "" && label != filterKategori {
			continue
		}
		for _, domain := range domains {
			items = append(items, checker.DomainEntry{Domain: domain, Label: label})
		}
	}

	if len(items) == 0 {
		return c.Send(fmt.Sprintf("📭 Tidak ada domain untuk kategori: *%s*", filterKategori), telebot.ModeMarkdown)
	}

	sortDomainEntries(items)
	return sendLongMessage(c, buildListText(items, filterKategori), menuMarkup())
}

// showListEditing — tampilkan list domain dengan edit in-place (untuk tombol List Domain)
// Untuk list pendek: edit pesan saat ini. Untuk list panjang: edit ke chunk pertama, send sisanya.
func (h *Handler) showListEditing(c telebot.Context, filterKategori string) error {
	chatID := c.Chat().ID
	urlsByLabel := h.ch.LoadURLs(chatID)

	if len(urlsByLabel) == 0 {
		return c.Edit("📭 Belum ada domain yang terdaftar.", menuMarkup(), telebot.ModeMarkdown)
	}

	var items []checker.DomainEntry
	for label, domains := range urlsByLabel {
		if filterKategori != "" && label != filterKategori {
			continue
		}
		for _, domain := range domains {
			items = append(items, checker.DomainEntry{Domain: domain, Label: label})
		}
	}

	if len(items) == 0 {
		return c.Edit(fmt.Sprintf("📭 Tidak ada domain untuk kategori: *%s*", filterKategori), menuMarkup(), telebot.ModeMarkdown)
	}

	sortDomainEntries(items)
	text := buildListText(items, filterKategori)

	const maxLen = 3800
	if len(text) <= maxLen {
		// Muat dalam satu pesan — edit in-place
		if err := c.Edit(text, menuMarkup(), telebot.ModeMarkdown); err == nil {
			return nil
		}
	}

	// Terlalu panjang atau edit gagal — hapus kategori message, kirim baru
	c.Delete()
	return sendLongMessage(c, text, menuMarkup())
}

func (h *Handler) handleCycle(c telebot.Context) error {
	return c.Send(h.buildCycleMessage(c.Chat().ID), telebot.ModeMarkdown)
}

func (h *Handler) handleBlocked(c telebot.Context) error {
	return c.Send(h.buildBlockedMessage(c.Chat().ID), telebot.ModeMarkdown)
}

func (h *Handler) handleForceBlock(c telebot.Context) error {
	chatID := c.Chat().ID
	state := h.ch.GetGroupState(chatID)
	if state == nil {
		return c.Send("❌ State tidak ditemukan")
	}
	if !state.AdminWhitelist[c.Sender().ID] {
		return c.Send("❌ Hanya admin yang bisa force block")
	}
	if len(c.Args()) < 1 {
		return c.Send("❌ Contoh: `/forceblock domain.com` atau `/forceblock domain.com KATEGORI`", telebot.ModeMarkdown)
	}

	domain := store.CleanDomain(c.Args()[0])
	label := "FORCEBLOCK"
	if len(c.Args()) >= 2 {
		label = strings.ToUpper(c.Args()[1])
	}

	h.st.AddForceBlock(domain, label)

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

	return c.Send(fmt.Sprintf(
		"🔒 *FORCE BLOCK ACTIVATED*\n\n"+
			"🌐 Domain: `%s`\n"+
			"📛 Label: `%s`\n"+
			"👤 By: %s\n\n"+
			"⚠️ Domain akan selalu dianggap BLOCKED",
		domain, label, getUserName(c.Sender()),
	), telebot.ModeMarkdown)
}

func (h *Handler) handleUnforceBlock(c telebot.Context) error {
	chatID := c.Chat().ID
	state := h.ch.GetGroupState(chatID)
	if state == nil {
		return c.Send("❌ State tidak ditemukan")
	}
	if !state.AdminWhitelist[c.Sender().ID] {
		return c.Send("❌ Hanya admin yang bisa unforce block")
	}
	if len(c.Args()) < 1 {
		return c.Send("❌ Contoh: `/unforceblock domain.com`", telebot.ModeMarkdown)
	}

	domain := store.CleanDomain(c.Args()[0])
	if h.st.RemoveForceBlock(domain) {
		return c.Send(fmt.Sprintf(
			"🔓 *FORCE BLOCK REMOVED*\n\n🌐 Domain: `%s`\n👤 By: %s\n\n✅ Domain mengikuti status API kembali",
			domain, getUserName(c.Sender()),
		), telebot.ModeMarkdown)
	}
	return c.Send(fmt.Sprintf("⚠️ Domain `%s` tidak ada di force block list", domain), telebot.ModeMarkdown)
}

func (h *Handler) handleForceList(c telebot.Context) error {
	chatID := c.Chat().ID
	state := h.ch.GetGroupState(chatID)
	if state == nil {
		return c.Send("❌ State tidak ditemukan")
	}
	if !state.AdminWhitelist[c.Sender().ID] {
		return c.Send("❌ Hanya admin yang bisa lihat force list")
	}

	forceList := h.st.GetForceList()
	if len(forceList) == 0 {
		return c.Send("📭 Tidak ada domain di force block list")
	}

	var sb strings.Builder
	sb.WriteString("🔒 *FORCE BLOCK LIST*\n═══════════════════════════\n\n")
	i := 1
	for domain, label := range forceList {
		sb.WriteString(fmt.Sprintf("%d. `%s` [%s]\n", i, domain, label))
		i++
	}
	sb.WriteString(fmt.Sprintf("\n📊 Total: *%d* domain", len(forceList)))
	return c.Send(sb.String(), telebot.ModeMarkdown)
}

func (h *Handler) handleResetBlock(c telebot.Context) error {
	chatID := c.Chat().ID
	state := h.ch.GetGroupState(chatID)
	if state == nil {
		return c.Send("❌ State tidak ditemukan")
	}
	if !state.AdminWhitelist[c.Sender().ID] {
		return c.Send("❌ Hanya admin yang bisa reset block")
	}
	if len(c.Args()) < 1 {
		return c.Send("❌ Contoh: `/resetblock domain.com`", telebot.ModeMarkdown)
	}

	domain := store.CleanDomain(c.Args()[0])
	stickyRemoved := h.st.RemoveStickyBlocked(domain)

	state.BlockedMu.Lock()
	_, blockedRemoved := state.BlockedDomains[domain]
	delete(state.BlockedDomains, domain)
	state.BlockedMu.Unlock()

	if stickyRemoved || blockedRemoved {
		return c.Send(fmt.Sprintf(
			"🔄 *BLOCK STATUS RESET*\n\n🌐 Domain: `%s`\n👤 By: %s\n\n✅ Domain akan dicek ulang dari API",
			domain, getUserName(c.Sender()),
		), telebot.ModeMarkdown)
	}
	return c.Send(fmt.Sprintf("⚠️ Domain `%s` tidak ada di blocked list", domain), telebot.ModeMarkdown)
}

func (h *Handler) handleStickyList(c telebot.Context) error {
	chatID := c.Chat().ID
	state := h.ch.GetGroupState(chatID)
	if state == nil {
		return c.Send("❌ State tidak ditemukan")
	}
	if !state.AdminWhitelist[c.Sender().ID] {
		return c.Send("❌ Hanya admin yang bisa lihat sticky list")
	}

	stickyList := h.st.GetStickyList()
	if len(stickyList) == 0 {
		return c.Send("📭 Tidak ada domain di sticky blocked list")
	}

	type domainTime struct {
		domain string
		t      time.Time
	}
	var sorted []domainTime
	for d, t := range stickyList {
		sorted = append(sorted, domainTime{d, t})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].t.After(sorted[j].t)
	})

	var sb strings.Builder
	sb.WriteString("🔒 *STICKY BLOCKED LIST*\n═══════════════════════════\n_(Domain yang pernah terdeteksi blocked)_\n\n")

	for i, dt := range sorted {
		if i >= 50 {
			sb.WriteString(fmt.Sprintf("\n... dan %d domain lainnya", len(sorted)-50))
			break
		}
		sb.WriteString(fmt.Sprintf("%d. `%s`\n    📅 %s\n", i+1, dt.domain, dt.t.Format("02 Jan 2006 15:04")))
	}

	sb.WriteString(fmt.Sprintf("\n📊 Total: *%d* domain\n", len(stickyList)))
	sb.WriteString("• `/resetblock domain.com` - Reset status")
	return c.Send(sb.String(), telebot.ModeMarkdown)
}

func (h *Handler) handleMyID(c telebot.Context) error {
	user := c.Sender()
	username := "@" + user.Username
	if user.Username == "" {
		username = "Tidak ada"
	}
	return c.Send(fmt.Sprintf(
		"👤 *INFORMASI AKUN ANDA*\n\n"+
			"🆔 ID: `%d`\n"+
			"📛 Nama: %s %s\n"+
			"🔗 Username: %s",
		user.ID, user.FirstName, user.LastName, username,
	), telebot.ModeMarkdown)
}

func (h *Handler) handleRestart(c telebot.Context) error {
	chatID := c.Chat().ID
	state := h.ch.GetGroupState(chatID)
	if state == nil {
		return c.Send("❌ State tidak ditemukan")
	}
	if !state.AdminWhitelist[c.Sender().ID] {
		return c.Send("❌ Hanya admin yang bisa restart")
	}
	c.Send("🔄 Restarting...")
	time.Sleep(2 * time.Second)
	os.Exit(0)
	return nil
}

func (h *Handler) handleMessage(c telebot.Context) error {
	chatID := c.Chat().ID
	state := h.ch.GetGroupState(chatID)
	if state == nil {
		return nil
	}

	user := c.Sender()
	if state.AdminWhitelist[user.ID] {
		return nil
	}

	now := time.Now()
	userID := user.ID

	state.ViolationMu.Lock()
	if state.UserViolations[userID] == nil {
		state.UserViolations[userID] = &checker.UserViolation{}
	}
	violation := state.UserViolations[userID]
	violation.Mu.Lock()

	var recent []time.Time
	for _, ts := range violation.Timestamps {
		if now.Sub(ts) < checker.TimeWindow {
			recent = append(recent, ts)
		}
	}
	recent = append(recent, now)
	violation.Timestamps = recent
	total := len(recent)

	violation.Mu.Unlock()
	state.ViolationMu.Unlock()

	if total >= checker.MaxViolations {
		c.Send(fmt.Sprintf("🚫 %dx spam = auto kick!", total))
		h.bot.Ban(&telebot.Chat{ID: chatID}, &telebot.ChatMember{User: user})
		state.ViolationMu.Lock()
		delete(state.UserViolations, userID)
		state.ViolationMu.Unlock()
	} else if total == 2 {
		c.Send("⚠️ Warning! 1x lagi = kick!")
	} else if total == 1 {
		c.Send("⚠️ Pesan tidak dikenali. 2x lagi = kick!")
	}
	return nil
}
