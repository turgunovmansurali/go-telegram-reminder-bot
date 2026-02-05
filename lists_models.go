package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

func main() {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		log.Fatal("❌ GEMINI_API_KEY topilmadi")
	}

	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		log.Fatal("❌ Client yaratilmadi:", err)
	}
	defer client.Close()

	fmt.Println("🔍 Gemini model ro‘yxati:")

	it := client.ListModels(ctx)
	for {
		m, err := it.Next()
		if err != nil {
			break
		}

		fmt.Println("—")
		fmt.Println("NAME:", m.Name)
		fmt.Println("DISPLAY:", m.DisplayName)
		fmt.Println("METHODS:", m.SupportedGenerationMethods)
	}
}

