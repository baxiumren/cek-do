package main

import (
	"log"
	"runtime"
	"time"

	"myapp/bot"
	"myapp/checker"
	"myapp/config"
	"myapp/store"

	"github.com/joho/godotenv"
	"gopkg.in/telebot.v3"
)

func init() {
	runtime.GOMAXPROCS(runtime.NumCPU())
}

func main() {
	log.Println("🚀 Starting KOMINFO Nawala Checker...")
	log.Printf("🔧 Using %d CPU cores", runtime.NumCPU())

	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ .env not found, using system env")
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatal("❌ Failed to load config:", err)
	}

	if len(cfg.Groups) == 0 {
		log.Fatal("❌ No groups configured")
	}

	st := store.New()
	st.LoadStickyBlocked()

	telegramBot, err := telebot.NewBot(telebot.Settings{
		Token:       cfg.Groups[0].BotToken,
		Poller:      &telebot.LongPoller{Timeout: 10 * time.Second},
		Synchronous: false,
	})
	if err != nil {
		log.Fatal("❌ Failed to create bot:", err)
	}

	// Build group states
	groupStates := make(map[int64]*checker.GroupState)
	for _, group := range cfg.Groups {
		state := &checker.GroupState{
			BlockedDomains: make(map[string]*checker.DomainAlertCycle),
			UserViolations: make(map[int64]*checker.UserViolation),
			AdminWhitelist: make(map[int64]bool),
		}
		for _, adminID := range group.AdminIDs {
			state.AdminWhitelist[adminID] = true
		}
		groupStates[group.GroupID] = state
		log.Printf("✅ Group %d ready (file: %s)", group.GroupID, group.URLsFile)
	}

	// Wire up handler (implements BotSender) and checker
	handler := bot.New(telegramBot, cfg, st, nil)
	ch := checker.New(cfg, st, handler)
	ch.SetGroupStates(groupStates)

	// Re-create handler with checker wired in
	handler = bot.New(telegramBot, cfg, st, ch)
	handler.Register()

	// Start background tasks
	for _, group := range cfg.Groups {
		go ch.StartAutoCheck(group.GroupID)
		go ch.StartSpamTask(group.GroupID)
	}
	go handler.CleanupTask()

	log.Println("🤖 Bot aktif dan siap melayani...")
	telegramBot.Start()
}
