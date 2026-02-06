package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/generative-ai-go/genai"
	_ "github.com/mattn/go-sqlite3"
	"google.golang.org/api/option"
	"gopkg.in/telebot.v3"
)

type AIResponse struct {
	Task string `json:"task"`
}

var (
	db     *sql.DB
	loc, _ = time.LoadLocation("Asia/Tashkent")

	timeRe = regexp.MustCompile(`(?i)(\d{1,2})[:.](\d{2})`)
	ctx    = context.Background()

	menu    = &telebot.ReplyMarkup{}
	btnList = menu.Text("📋 Kutilayotgan")
)

func main() {
	// ===== ENV =====
	tgToken := os.Getenv("TELEGRAM_APITOKEN")
	aiKey := os.Getenv("GEMINI_API_KEY")
	port := os.Getenv("PORT")
	if port == "" {
		port = "10000"
	}

	if tgToken == "" || aiKey == "" {
		log.Fatal("❌ Tokenlar topilmadi")
	}

	// ===== SQLITE =====
	var err error
	db, err = sql.Open("sqlite3", "./reminders.db")
	if err != nil {
		log.Fatal(err)
	}
	initDB()

	// ===== BOT =====
	bot, err := telebot.NewBot(telebot.Settings{
		Token:  tgToken,
		Poller: &telebot.LongPoller{Timeout: 10 * time.Second},
	})
	if err != nil {
		log.Fatal(err)
	}

	menu.Reply(menu.Row(btnList))

	// ===== GEMINI =====
	aiClient, _ := genai.NewClient(ctx, option.WithAPIKey(aiKey))
	defer aiClient.Close()
	model := aiClient.GenerativeModel("models/gemini-flash-latest")

	// ===== START =====
	bot.Handle("/start", func(c telebot.Context) error {
		return c.Send(
			"👋 Salom!\n\n"+
				"Men ⏰ *aqlli eslatma botman*.\n"+
				"Menga vaqt bilan yozing.\n\n"+
				"_Masalan:_\n`12:00 da darsim bor`\n`07:00 da menga uyg'onishni eslatib yubor`",
			menu,
			telebot.ModeMarkdown,
		)
	})

	// ===== KUTILAYOTGAN =====
	bot.Handle(&btnList, func(c telebot.Context) error {
		return showPending(c)
	})
	bot.Handle("/kutilayotgan", func(c telebot.Context) error {
		return showPending(c)
	})

	// ===== ASOSIY MATN =====
	commandWords := []string{"eslat", "ayt", "yubor", "bildir", "xabar"}

	bot.Handle(telebot.OnText, func(c telebot.Context) error {
		text := strings.ToLower(strings.TrimSpace(c.Text()))

		match := timeRe.FindStringSubmatch(text)
		if len(match) != 3 {
			return c.Send("🙂 Iltimos, vaqtni ham yozing.", menu)
		}

		h, _ := strconv.Atoi(match[1])
		m, _ := strconv.Atoi(match[2])

		now := time.Now().In(loc)
		fire := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, loc)

		// ==== O‘TIB KETGAN VAQT ====
		if fire.Before(now) {
			yesNo := &telebot.ReplyMarkup{}
			btnYes := yesNo.Data("Ha", "tomorrow_yes", text)
			btnNo := yesNo.Data("Yo‘q", "tomorrow_no")
			yesNo.Inline(yesNo.Row(btnYes, btnNo))

			return c.Send(
				"⚠️ Bu vaqt allaqachon o‘tib ketgan.\nErtaga shu vaqtda eslataymi?",
				yesNo,
			)
		}

		task := cleanTask(text, commandWords, model)
		saveReminder(c.Chat().ID, task, fire)

		return c.Send(
			fmt.Sprintf("✅ Eslatma qo‘shildi\n🕒 %02d:%02d\n📝 %s", h, m, task),
			menu,
		)
	})

	// ===== CALLBACK: ERTAGA HA =====
	bot.Handle(&telebot.Callback{Unique: "tomorrow_yes"}, func(c telebot.Context) error {
		text := c.Callback().Data

		match := timeRe.FindStringSubmatch(text)
		if len(match) != 3 {
			return c.Edit("❌ Vaqtni aniqlab bo‘lmadi")
		}

		h, _ := strconv.Atoi(match[1])
		m, _ := strconv.Atoi(match[2])

		fire := time.Date(
			time.Now().In(loc).Year(),
			time.Now().In(loc).Month(),
			time.Now().In(loc).Day()+1,
			h, m, 0, 0, loc,
		)

		task := cleanTask(text, []string{"eslat", "ayt", "yubor"}, nil)
		saveReminder(c.Chat().ID, task, fire)

		return c.Edit("✅ Eslatma ertangi kunga qo‘shildi")
	})

	// ===== CALLBACK: YO‘Q =====
	bot.Handle(&telebot.Callback{Unique: "tomorrow_no"}, func(c telebot.Context) error {
		return c.Edit("❎ Eslatma bekor qilindi")
	})

	// ===== HTTP (RENDER) =====
	go func() {
		http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte("OK"))
		})
		log.Println("🌐 HTTP server ishga tushdi, port:", port)
		http.ListenAndServe(":"+port, nil)
	}()

	// ===== WORKER =====
	go reminderWorker(bot)

	log.Println("🤖 Bot ishga tushdi")
	bot.Start()
}

// ================= DB =================

func initDB() {
	db.Exec(`
	CREATE TABLE IF NOT EXISTS reminders (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		chat_id INTEGER,
		task TEXT,
		fire_at DATETIME
	)
	`)
}

func saveReminder(chatID int64, task string, fire time.Time) {
	db.Exec(`INSERT INTO reminders(chat_id, task, fire_at) VALUES(?,?,?)`,
		chatID, task, fire)
}

// ================= WORKER =================

func reminderWorker(bot *telebot.Bot) {
	for {
		rows, _ := db.Query(`SELECT id, chat_id, task FROM reminders WHERE fire_at <= ?`,
			time.Now().In(loc))
		for rows.Next() {
			var id int
			var chatID int64
			var task string
			rows.Scan(&id, &chatID, &task)

			bot.Send(
				telebot.ChatID(chatID),
				fmt.Sprintf("🔔 *ESLATMA!*\n%s\n_soat %s bo‘ldi_",
					task, time.Now().Format("15:04")),
				menu,
				telebot.ModeMarkdown,
			)
			db.Exec(`DELETE FROM reminders WHERE id=?`, id)
		}
		time.Sleep(20 * time.Second)
	}
}

// ================= LIST =================

func showPending(c telebot.Context) error {
	rows, _ := db.Query(
		`SELECT id, task, fire_at FROM reminders WHERE chat_id=? ORDER BY fire_at`,
		c.Chat().ID,
	)
	defer rows.Close()

	msg := "📋 *Kutilayotgan bildirishnomalar:*\n\n"
	count := 0

	for rows.Next() {
		var id int
		var task string
		var fire time.Time
		rows.Scan(&id, &task, &fire)

		msg += fmt.Sprintf("🕒 %s — %s → /ochir_%d\n",
			fire.Format("15:04"), task, id)
		count++
	}

	if count == 0 {
		return c.Send("🙂 Sizda kutilayotgan xabarlar yo‘q", menu)
	}

	return c.Send(msg, menu, telebot.ModeMarkdown)
}

// ================= TASK CLEAN =================

func cleanTask(text string, cmds []string, model *genai.GenerativeModel) string {
	for _, w := range cmds {
		if strings.Contains(text, w) && model != nil {
			prompt := fmt.Sprintf(`{"task":"qisqa"}\nMatn: "%s"`, text)
			resp, err := model.GenerateContent(ctx, genai.Text(prompt))
			if err == nil && len(resp.Candidates) > 0 {
				raw := fmt.Sprint(resp.Candidates[0].Content.Parts[0])
				raw = strings.Trim(raw, "` \njson")
				var ai AIResponse
				if json.Unmarshal([]byte(raw), &ai) == nil {
					return ai.Task
				}
			}
		}
	}

	task := timeRe.ReplaceAllString(text, "")
	task = strings.ReplaceAll(task, "soat", "")
	reDa := regexp.MustCompile(`\bda\b`)
	task = reDa.ReplaceAllString(task, "")
	return strings.Join(strings.Fields(task), " ")
}

