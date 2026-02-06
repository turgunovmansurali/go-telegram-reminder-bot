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
	Time string `json:"time"`
	Task string `json:"task"`
}

type PendingConfirm struct {
	ChatID int64
	Time   string
	Text   string
}

var pending = map[int64]PendingConfirm{}

func main() {
	loc, _ := time.LoadLocation("Asia/Tashkent")

	tgToken := os.Getenv("TELEGRAM_APITOKEN")
	aiKey := os.Getenv("GEMINI_API_KEY")

	if tgToken == "" || aiKey == "" {
		log.Fatal("Tokenlar topilmadi")
	}

	// === SQLite ===
	db, err := sql.Open("sqlite3", "bot.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	db.Exec(`
	CREATE TABLE IF NOT EXISTS reminders (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		chat_id INTEGER,
		fire_at INTEGER,
		task TEXT
	)`)

	// === Telegram bot ===
	bot, _ := telebot.NewBot(telebot.Settings{
		Token:  tgToken,
		Poller: &telebot.LongPoller{Timeout: 10 * time.Second},
	})

	// === Gemini ===
	ctx := context.Background()
	aiClient, _ := genai.NewClient(ctx, option.WithAPIKey(aiKey))
	defer aiClient.Close()
	model := aiClient.GenerativeModel("models/gemini-flash-latest")

	// === Tugmalar ===
	menuConfirm := &telebot.ReplyMarkup{}
	btnYes := menuConfirm.Data("Ha ✅", "yes")
	btnNo := menuConfirm.Data("Yo‘q ❌", "no")
	menuConfirm.Inline(menuConfirm.Row(btnYes, btnNo))

	// === /start ===
	bot.Handle("/start", func(c telebot.Context) error {
		args := c.Args()
		if len(args) == 1 && strings.HasPrefix(args[0], "del_") {
			id := strings.TrimPrefix(args[0], "del_")
			db.Exec("DELETE FROM reminders WHERE id = ? AND chat_id = ?", id, c.Chat().ID)
			return c.Send("🗑 Eslatma o‘chirildi.")
		}

		return c.Send(
			"👋 Salom!\n\nMen ⏰ aqlli eslatma botman.\n\nMisol:\n`12:00 da darsim bor`\n`07:00 da uyg‘onishni eslatib yubor`",
			telebot.ModeMarkdown,
		)
	})

	// === /kutilayotgan ===
	bot.Handle("/kutilayotgan", func(c telebot.Context) error {
		rows, _ := db.Query(
			"SELECT id, fire_at, task FROM reminders WHERE chat_id = ? ORDER BY fire_at",
			c.Chat().ID,
		)
		defer rows.Close()

		var out strings.Builder
		for rows.Next() {
			var id int
			var fireAt int64
			var task string
			rows.Scan(&id, &fireAt, &task)

			t := time.Unix(fireAt, 0).In(loc).Format("15:04")
			link := fmt.Sprintf("https://t.me/%s?start=del_%d", bot.Me.Username, id)

			out.WriteString(fmt.Sprintf(
				"🕒 %s — %s → [o‘chirish](%s)\n",
				t, task, link,
			))
		}

		if out.Len() == 0 {
			return c.Send("🙂 Sizda hozircha kutilayotgan eslatmalar yo‘q.")
		}

		return c.Send(
			"*Kutilayotgan eslatmalar:*\n\n"+out.String(),
			telebot.ModeMarkdown,
		)
	})

	timeRe := regexp.MustCompile(`(\d{1,2}):(\d{2})`)
	commands := []string{"eslat", "ayt", "yubor", "bildir"}

	bot.Handle(telebot.OnText, func(c telebot.Context) error {
		text := strings.ToLower(strings.TrimSpace(c.Text()))
		m := timeRe.FindStringSubmatch(text)
		if len(m) != 3 {
			return c.Send("🙂 Iltimos, vaqtni ham yozing. Masalan: `12:00 da darsim bor`")
		}

		h, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])

		now := time.Now().In(loc)
		fire := time.Date(now.Year(), now.Month(), now.Day(), h, min, 0, 0, loc)

		if fire.Before(now) {
			pending[c.Chat().ID] = PendingConfirm{
				ChatID: c.Chat().ID,
				Time:   fmt.Sprintf("%02d:%02d", h, min),
				Text:   text,
			}
			return c.Send(
				"⚠️ Bu vaqt allaqachon o‘tib ketgan.\nErtaga shu vaqtda eslataymi?",
				menuConfirm,
			)
		}

		task := extractTask(text, commands, model, ctx)

		db.Exec(
			"INSERT INTO reminders(chat_id, fire_at, task) VALUES(?,?,?)",
			c.Chat().ID,
			fire.Unix(),
			task,
		)

		go schedule(bot, c.Chat().ID, fire, task, db)

		return c.Send(fmt.Sprintf("✅ Eslatma qo‘shildi\n🕒 %02d:%02d\n📝 %s", h, min, task))
	})

	bot.Handle(&btnYes, func(c telebot.Context) error {
		p, ok := pending[c.Chat().ID]
		if !ok {
			return c.Respond()
		}

		delete(pending, c.Chat().ID)

		tp := strings.Split(p.Time, ":")
		h, _ := strconv.Atoi(tp[0])
		m, _ := strconv.Atoi(tp[1])

		now := time.Now().In(loc)
		fire := time.Date(now.Year(), now.Month(), now.Day()+1, h, m, 0, 0, loc)

		task := extractTask(p.Text, commands, model, ctx)

		db.Exec(
			"INSERT INTO reminders(chat_id, fire_at, task) VALUES(?,?,?)",
			c.Chat().ID,
			fire.Unix(),
			task,
		)

		go schedule(bot, c.Chat().ID, fire, task, db)

		return c.Edit("👍 Yaxshi, ertaga eslataman.")
	})

	bot.Handle(&btnNo, func(c telebot.Context) error {
		delete(pending, c.Chat().ID)
		return c.Edit("🙂 Mayli, bekor qilindi.")
	})

	// === HTTP server (Render uchun) ===
	go func() {
		port := os.Getenv("PORT")
		if port == "" {
			port = "8080"
		}
		http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte("OK"))
		})
		http.ListenAndServe(":"+port, nil)
	}()

	log.Println("🤖 Bot ishga tushdi")
	bot.Start()
}

func extractTask(text string, commands []string, model *genai.GenerativeModel, ctx context.Context) string {
	for _, c := range commands {
		if strings.Contains(text, c) {
			prompt := fmt.Sprintf(
				`Faqat JSON qaytar: {"task":"qisqa vazifa"}\nMatn: "%s"`,
				text,
			)
			resp, err := model.GenerateContent(ctx, genai.Text(prompt))
			if err == nil {
				raw := fmt.Sprint(resp.Candidates[0].Content.Parts[0])
				raw = strings.Trim(raw, "` \njson")
				var a AIResponse
				if json.Unmarshal([]byte(raw), &a) == nil {
					return a.Task
				}
			}
		}
	}
	t := timeRe.ReplaceAllString(text, "")
	t = strings.ReplaceAll(t, "soat", "")
	reDa := regexp.MustCompile(`\bda\b`)
	t = reDa.ReplaceAllString(t, "")
	return strings.Join(strings.Fields(t), " ")
}

func schedule(bot *telebot.Bot, chatID int64, fire time.Time, task string, db *sql.DB) {
	time.Sleep(time.Until(fire))
	bot.Send(telebot.ChatID(chatID),
		fmt.Sprintf("🔔 ESLATMA!\n%s\nsoat %s bo‘ldi",
			task,
			time.Now().Format("15:04"),
		),
	)
	db.Exec("DELETE FROM reminders WHERE chat_id = ? AND fire_at = ?", chatID, fire.Unix())
}

