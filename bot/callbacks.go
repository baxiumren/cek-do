package bot

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"

	"myapp/checker"
	"myapp/store"
	"gopkg.in/telebot.v3"
)

// ==================== SHARED MESSAGE BUILDERS ====================

func (h *Handler) buildInfoMessage(chatID int64) string {
	state := h.ch.GetGroupState(chatID)
	if state == nil {
		return "❌ State tidak ditemukan"
	}

	urlsByLabel := h.ch.LoadURLs(chatID)

	var sb strings.Builder
	sb.WriteString("📊 *STATISTIK BOT KOMINFO*\n")
	sb.WriteString("═══════════════════════════════\n\n")

	totalDomains := 0
	var labels []string
	for label := range urlsByLabel {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	for _, label := range labels {
		sb.WriteString(fmt.Sprintf("• %s: %d domain\n", label, len(urlsByLabel[label])))
		totalDomains += len(urlsByLabel[label])
	}

	sb.WriteString(fmt.Sprintf("\n📈 Total Domain: *%d*\n", totalDomains))

	state.BlockedMu.RLock()
	activeAlerts, inCooldown := 0, 0
	for _, cycle := range state.BlockedDomains {
		if !cycle.InCooldown {
			activeAlerts++
		} else {
			inCooldown++
		}
	}
	sb.WriteString(fmt.Sprintf("🚨 Alert Aktif: *%d* domain\n", activeAlerts))
	sb.WriteString(fmt.Sprintf("⏸️ Dalam Cooldown: *%d* domain\n", inCooldown))
	state.BlockedMu.RUnlock()

	sb.WriteString(fmt.Sprintf("\n⏱️ Alert Durasi: *%.0f menit*\n", checker.AlertDuration.Minutes()))
	sb.WriteString(fmt.Sprintf("⏳ Cooldown: *%.0f menit*\n", checker.CooldownDuration.Minutes()))
	sb.WriteString(fmt.Sprintf("📡 Interval Cek: *%.0f detik*\n", checker.CheckInterval.Seconds()))
	sb.WriteString(fmt.Sprintf("⚡ Parallel: *%d* domain/batch\n", checker.MaxConcurrentChecks))
	sb.WriteString(fmt.Sprintf("🔧 CPU Cores: *%d*\n", runtime.NumCPU()))
	sb.WriteString(fmt.Sprintf("🔒 Force Block: *%d* domain\n", len(h.st.GetForceList())))
	sb.WriteString(fmt.Sprintf("📌 Sticky Block: *%d* domain\n", len(h.st.GetStickyList())))
	sb.WriteString(fmt.Sprintf("👤 Group ID: `%d`\n", chatID))
	sb.WriteString(fmt.Sprintf("👑 Admin: *%d* user", len(state.AdminWhitelist)))

	return sb.String()
}

func (h *Handler) buildBlockedMessage(chatID int64) string {
	state := h.ch.GetGroupState(chatID)
	if state == nil {
		return "❌ State tidak ditemukan"
	}

	state.BlockedMu.RLock()
	defer state.BlockedMu.RUnlock()

	if len(state.BlockedDomains) == 0 {
		return "✅ Tidak ada domain yang diblokir"
	}

	var domains []string
	for domain := range state.BlockedDomains {
		domains = append(domains, domain)
	}
	sort.Strings(domains)

	var sb strings.Builder
	sb.WriteString("🚫 *DOMAIN TERBLOKIR KOMINFO*\n═══════════════════════════════\n\n")
	sb.WriteString(fmt.Sprintf("📊 Total: *%d* domain\n\n", len(domains)))
	for i, domain := range domains {
		cycle := state.BlockedDomains[domain]
		status := "🔴 AKTIF"
		if cycle.InCooldown {
			status = "⏸️ COOLDOWN"
		}
		sb.WriteString(fmt.Sprintf("%d. `%s` [%s]\n", i+1, domain, status))
	}
	sb.WriteString("\n⚠️ Alert Cycle: 2m aktif → 10m jeda")
	return sb.String()
}

func (h *Handler) buildCycleMessage(chatID int64) string {
	state := h.ch.GetGroupState(chatID)
	if state == nil {
		return "❌ State tidak ditemukan"
	}

	state.BlockedMu.RLock()
	defer state.BlockedMu.RUnlock()

	if len(state.BlockedDomains) == 0 {
		return "🟢 Tidak ada domain dalam cycle alert"
	}

	var sb strings.Builder
	sb.WriteString("🔄 *DOMAIN ALERT CYCLE*\n══════════════════════\n\n")

	now := time.Now()
	var activeDomains, cooldownDomains []string
	for domain, cycle := range state.BlockedDomains {
		if !cycle.InCooldown {
			activeDomains = append(activeDomains, domain)
		} else {
			_ = cycle
			cooldownDomains = append(cooldownDomains, domain)
		}
	}
	sort.Strings(activeDomains)
	sort.Strings(cooldownDomains)

	if len(activeDomains) > 0 {
		sb.WriteString("🔴 *ALERT AKTIF:*\n")
		for _, domain := range activeDomains {
			cycle := state.BlockedDomains[domain]
			timeLeft := checker.AlertDuration - now.Sub(cycle.CycleStartTime)
			if timeLeft > 0 {
				sb.WriteString(fmt.Sprintf("• `%s` - Alert #%d (%.0fs lagi)\n",
					domain, cycle.AlertCount, timeLeft.Seconds()))
			}
		}
		sb.WriteString("\n")
	}

	if len(cooldownDomains) > 0 {
		sb.WriteString("⏸️ *SEDANG COOLDOWN:*\n")
		for _, domain := range cooldownDomains {
			cycle := state.BlockedDomains[domain]
			cooldownLeft := checker.CooldownDuration - now.Sub(cycle.CycleStartTime)
			if cooldownLeft > 0 {
				sb.WriteString(fmt.Sprintf("• `%s` - %.1f menit lagi\n", domain, cooldownLeft.Minutes()))
			} else {
				sb.WriteString(fmt.Sprintf("• `%s` - READY untuk cycle baru\n", domain))
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString(fmt.Sprintf("📊 Total: %d | Aktif: %d | Cooldown: %d",
		len(state.BlockedDomains), len(activeDomains), len(cooldownDomains)))

	return sb.String()
}

// ==================== INLINE MENUS ====================

func checkResultMenu(domain string) *telebot.ReplyMarkup {
	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("🗑️ Hapus Domain", "removedom", domain)),
	)
	return menu
}

// ==================== ACTION CALLBACKS (domain-specific) ====================

func (h *Handler) registerCallbacks() {
	h.bot.Handle("\fresetblock", h.cbResetBlock)
	h.bot.Handle("\fremovedom", h.cbRemoveDomain)
}

func (h *Handler) cbResetBlock(c telebot.Context) error {
	chatID := c.Chat().ID
	state := h.ch.GetGroupState(chatID)
	if state == nil {
		return c.Respond(&telebot.CallbackResponse{Text: "❌ State tidak ditemukan"})
	}
	if !state.AdminWhitelist[c.Sender().ID] {
		return c.Respond(&telebot.CallbackResponse{Text: "❌ Hanya admin yang bisa reset block"})
	}

	domain := c.Data()
	stickyRemoved := h.st.RemoveStickyBlocked(domain)

	state.BlockedMu.Lock()
	_, blockedRemoved := state.BlockedDomains[domain]
	delete(state.BlockedDomains, domain)
	state.BlockedMu.Unlock()

	if stickyRemoved || blockedRemoved {
		c.Respond(&telebot.CallbackResponse{Text: "✅ Block status direset!"})
		return c.Edit(fmt.Sprintf(
			"🔄 *BLOCK STATUS RESET*\n\n🌐 Domain: `%s`\n👤 By: %s\n\n✅ Domain akan dicek ulang dari API",
			domain, getUserName(c.Sender()),
		), telebot.ModeMarkdown)
	}
	return c.Respond(&telebot.CallbackResponse{Text: "⚠️ Domain tidak ada di blocked list"})
}

func (h *Handler) cbRemoveDomain(c telebot.Context) error {
	chatID := c.Chat().ID
	state := h.ch.GetGroupState(chatID)
	if state == nil {
		return c.Respond(&telebot.CallbackResponse{Text: "❌ State tidak ditemukan"})
	}
	if !state.AdminWhitelist[c.Sender().ID] {
		return c.Respond(&telebot.CallbackResponse{Text: "❌ Hanya admin yang bisa hapus domain"})
	}

	domain := c.Data()
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
		// Domain sudah tidak ada — hapus tombol dari pesan supaya tidak bisa diklik lagi
		c.Respond(&telebot.CallbackResponse{Text: "⚠️ Domain sudah tidak ada di list"})
		c.Edit(c.Message().Text, telebot.ModeMarkdown) // edit tanpa inline keyboard
		return nil
	}

	if err := h.ch.SaveURLs(chatID, urlsByLabel); err != nil {
		return c.Respond(&telebot.CallbackResponse{Text: "❌ Gagal menyimpan"})
	}

	state.BlockedMu.Lock()
	delete(state.BlockedDomains, domain)
	state.BlockedMu.Unlock()

	h.st.RemoveStickyBlocked(domain)

	c.Respond(&telebot.CallbackResponse{Text: "✅ Domain dihapus!"})
	return c.Edit(fmt.Sprintf(
		"🗑️ *DOMAIN DIHAPUS*\n\n🌐 Domain: `%s`\n📂 Kategori: *%s*\n👤 By: %s\n\n✅ Alert cycle dihentikan",
		domain, domainLabel, getUserName(c.Sender()),
	), telebot.ModeMarkdown)
}

// ==================== HELPERS ====================

func buildListText(items []checker.DomainEntry, category string) string {
	var sb strings.Builder
	if category == "" || category == "ALL" {
		sb.WriteString("📋 *Daftar Semua Domain*\n")
	} else {
		sb.WriteString(fmt.Sprintf("📋 *Daftar Domain - %s*\n", category))
	}
	sb.WriteString("══════════════════════\n")
	for i, item := range items {
		sb.WriteString(fmt.Sprintf("%d. `[%s]` %s\n", i+1, item.Label, item.Domain))
	}
	sb.WriteString(fmt.Sprintf("\n📊 Total: *%d* domain", len(items)))
	return sb.String()
}

func sortDomainEntries(items []checker.DomainEntry) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Label != items[j].Label {
			return items[i].Label < items[j].Label
		}
		return items[i].Domain < items[j].Domain
	})
}

// unused import guard
var _ = store.CleanDomain
