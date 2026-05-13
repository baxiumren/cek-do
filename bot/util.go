package bot

import (
	"fmt"
	"strings"
	"time"

	"myapp/checker"
	"gopkg.in/telebot.v3"
)


func getUserName(user *telebot.User) string {
	if user.Username != "" {
		return "@" + user.Username
	}
	return user.FirstName
}

// sendLongMessage kirim pesan panjang, potong kalau melebihi batas Telegram.
// opts bisa diisi *telebot.ReplyMarkup untuk keyboard (hanya di-attach ke pesan terakhir).
func sendLongMessage(c telebot.Context, text string, opts ...interface{}) error {
	const maxLen = 3800
	if len(text) <= maxLen {
		args := append([]interface{}{telebot.ModeMarkdown}, opts...)
		if err := c.Send(text, args...); err != nil {
			return c.Send(text)
		}
		return nil
	}

	lines := strings.Split(text, "\n")
	chunk := ""
	partNum := 1
	var chunks []string

	for _, line := range lines {
		if len(chunk)+len(line)+1 > maxLen {
			chunks = append(chunks, chunk)
			partNum++
			chunk = fmt.Sprintf("📋 *(lanjutan bag. %d)*\n", partNum)
		}
		chunk += line + "\n"
	}
	if strings.TrimSpace(chunk) != "" {
		chunks = append(chunks, chunk)
	}

	for i, ch := range chunks {
		var args []interface{}
		args = append(args, telebot.ModeMarkdown)
		// Attach keyboard hanya di pesan terakhir
		if i == len(chunks)-1 {
			args = append(args, opts...)
		}
		if err := c.Send(ch, args...); err != nil {
			c.Send(ch)
		}
	}
	return nil
}

// CleanupTask membersihkan UserViolations dan WhatsApp cooldown secara periodik
func (h *Handler) CleanupTask() {
	for {
		time.Sleep(30 * time.Minute)

		for _, state := range h.ch.GetGroupStates() {
			state.ViolationMu.Lock()
			now := time.Now()

			var toDelete []int64
			toUpdate := make(map[int64][]time.Time)

			for userID, violation := range state.UserViolations {
				violation.Mu.RLock()
				var recent []time.Time
				for _, ts := range violation.Timestamps {
					if now.Sub(ts) < checker.TimeWindow {
						recent = append(recent, ts)
					}
				}
				violation.Mu.RUnlock()

				if len(recent) == 0 {
					toDelete = append(toDelete, userID)
				} else {
					toUpdate[userID] = recent
				}
			}

			for userID, recent := range toUpdate {
				if v, exists := state.UserViolations[userID]; exists {
					v.Mu.Lock()
					v.Timestamps = recent
					v.Mu.Unlock()
				}
			}
			for _, userID := range toDelete {
				delete(state.UserViolations, userID)
			}

			state.ViolationMu.Unlock()
		}

	}
}
