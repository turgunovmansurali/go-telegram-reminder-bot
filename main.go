package main

import (
	"context"
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
	"google.golang.org/api/option"
	"gopkg.in/telebot.v3"
)

type AIResponse struct {
	Time string `json:"time"`
	Task string `json:"task"`
}

func main() {
	// === Timezone ===
	loc, _ := time.LoadLocation("Asia/Tashkent")

	// === ENV ===
	tgToken := os.Getenv("TELEGRAM_APITOKEN")
	aiKey := os.Getenv("GEMINI_API_KEY")

	if tgToken == "" || aiKey == "" {
		log.Fatal("❌ Tokenlar topilmadi (ENV)")
	}

	// === Telegram bot ===
	bot, err := telebot.NewBot(telebot.Settings{
		Token:  tgToken,
		Poller: &telebot.LongPoller{Timeout: 10 * time.Second},
	})
	if err != nil {
		log.Fatal(err)
	}

	// === Gemini client ===
	ctx := context.Background()
	aiClient, err := genai.NewClient(ctx, option.WithAPIKey(aiKey))
	if err != nil {
		log.Fatal(err)
	}
	defer aiClient.Close()

	model := aiClient.GenerativeModel("models/gemini-flash-latest")

	// === Button ===
	menu := &telebot.ReplyMarkup{}
	btnThanks := menu.Data("Rahmat 👍🏻", "thanks")
	menu.Inline(menu.Row(btnThanks))

	// === /start ===
	bot.Handle("/start", func(c telebot.Context) error {
		return c.Send(
			"👋 Salom!\n\n"+
				"Men ⏰ *aqlli eslatma botman*.\n"+
				"Menga vaqt bilan yozing, men o‘sha vaqtda eslataman.\n\n"+
				"_Masalan:_\n"+
				"`12:00 da darsim bor`\n"+
				"`07:00 da menga uyg'onishni eslatib yubor`",
			telebot.ModeMarkdown,
		)
	})

	// === Regex & commands ===
	timeRe := regexp.MustCompile(`(?i)(\d{1,2})[:.](\d{2})`)
	commandWords := []string{
		"eslat", "ayt", "aytgin", "yubor", "bildir", "xabar ber",
	}

	// === Text handler ===
	bot.Handle(telebot.OnText, func(c telebot.Context) error {
		text := strings.ToLower(strings.TrimSpace(c.Text()))

		match := timeRe.FindStringSubmatch(text)
		if len(match) != 3 {
			return c.Send(
				"🙂 Iltimos, vaqtni ham yozing.\nMasalan: `07:00 da uyg'onish`",
				telebot.ModeMarkdown,
			)
		}

		hour, _ := strconv.Atoi(match[1])
		minute, _ := strconv.Atoi(match[2])

		hasCommand := false
		for _, w := range commandWords {
			if strings.Contains(text, w) {
				hasCommand = true
				break
			}
		}

		var task string

		// === AI ishlaydigan holat ===
		if hasCommand {
			prompt := fmt.Sprintf(`
Foydalanuvchi xabari: "%s"

Faqat JSON qaytar:
{"time":"HH:mm","task":"1-3 so‘zli qisqa vazifa"}
`, text)

			resp, err := model.GenerateContent(ctx, genai.Text(prompt))
			if err != nil {
				if strings.Contains(err.Error(), "429") {
					return c.Send("⚠️ AI limiti vaqtincha tugagan.")
				}
				log.Println("AI ERROR:", err)
				return c.Send("⚠️ AI xatosi.")
			}

			raw := fmt.Sprint(resp.Candidates[0].Content.Parts[0])
			raw = strings.Trim(raw, "` \njson")

			var ai AIResponse
			if err := json.Unmarshal([]byte(raw), &ai); err != nil {
				return c.Send("⚠️ AI javobini tushunmadim.")
			}

			task = ai.Task
		} else {
			// === Oddiy eslatma (AI YO‘Q) ===
			task = strings.TrimSpace(timeRe.ReplaceAllString(text, ""))
			task = strings.ReplaceAll(task, "soat", "")
			reDa := regexp.MustCompile(`\bda\b`)
			task = reDa.ReplaceAllString(task, "")
			task = strings.Join(strings.Fields(task), " ")
		}

		if task == "" {
			task = "Eslatma vaqti bo‘ldi"
		}

		now := time.Now().In(loc)
		fire := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, loc)
		if fire.Before(now) {
			fire = fire.Add(24 * time.Hour)
		}

		c.Send(fmt.Sprintf(
			"✅ Eslatma qo‘shildi\n🕒 %02d:%02d\n📝 %s",
			hour, minute, task,
		))

		go func(chatID int64, d time.Duration, msg string) {
			time.Sleep(d)
			now := time.Now().In(loc)
			bot.Send(
				telebot.ChatID(chatID),
				"🔔 *ESLATMA!*\n"+
					msg+"\n"+
					"soat "+now.Format("15:04")+" bo‘ldi",
				menu,
				telebot.ModeMarkdown,
			)
		}(c.Chat().ID, time.Until(fire), task)

		return nil
	})

	bot.Handle(&btnThanks, func(c telebot.Context) error {
		return c.Edit("😊 Arzimaydi, yordam berganimdan xursandman!")
	})

	// === START BOT ===
	log.Println("🤖 Bot ishga tushdi")
	go bot.Start()

	// === HTTP SERVER (Render Web Service uchun) ===
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	log.Println("🌐 HTTP server ishga tushdi, port:", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

