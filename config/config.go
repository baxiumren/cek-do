package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
)

// ==================== CONFIG STRUCTURES ====================

type GroupConfig struct {
	GroupID  int64
	BotToken string
	AdminIDs []int64
	URLsFile string
}

type Config struct {
	Groups []GroupConfig
}

// ==================== LOAD CONFIG ====================

func Load() (*Config, error) {
	cfg := &Config{}
	i := 1
	for {
		token := os.Getenv(fmt.Sprintf("GROUP_%d_BOT_TOKEN", i))
		if token == "" {
			break
		}

		chatIDStr := os.Getenv(fmt.Sprintf("GROUP_%d_CHAT_ID", i))
		if chatIDStr == "" {
			log.Printf("⚠️ Group %d: No CHAT_ID", i)
			i++
			continue
		}

		chatID, err := strconv.ParseInt(chatIDStr, 10, 64)
		if err != nil {
			log.Printf("⚠️ Group %d: Invalid CHAT_ID", i)
			i++
			continue
		}

		var adminIDs []int64
		adminIDsStr := os.Getenv(fmt.Sprintf("GROUP_%d_ADMIN_IDS", i))
		if adminIDsStr != "" {
			for _, idStr := range strings.Split(adminIDsStr, ",") {
				idStr = strings.TrimSpace(idStr)
				if id, err := strconv.ParseInt(idStr, 10, 64); err == nil {
					adminIDs = append(adminIDs, id)
				}
			}
		}

		cfg.Groups = append(cfg.Groups, GroupConfig{
			GroupID:  chatID,
			BotToken: token,
			AdminIDs: adminIDs,
			URLsFile: fmt.Sprintf("urls_grup_%d.txt", i),
		})

		log.Printf("✅ Group %d: ID=%d, Admins=%d, File=%s", i, chatID, len(adminIDs), fmt.Sprintf("urls_grup_%d.txt", i))
		i++
	}
	return cfg, nil
}
