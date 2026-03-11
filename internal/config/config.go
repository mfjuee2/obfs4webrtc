package config

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/chromedp/chromedp"
	"github.com/joho/godotenv"
)

// Config структура для сохранения
type Config struct {
	VkCookiesPath string
	VkTargetID    string
	VkProfilePath string
	Mode          string // "server" или "client"
}

func RunSetupWizardVk(mode string) error {
	fmt.Println("=== VK Tunnel Setup ===")
	fmt.Println("Сейчас откроется окно браузера.")
	fmt.Println("Пожалуйста, войдите в свой аккаунт ВКонтакте в этом окне.")
	fmt.Println("Как только вы войдете (увидите ленту новостей), программа автоматически считает ключи.")
	fmt.Println("Нажмите Enter, чтобы начать...")
	bufio.NewReader(os.Stdin).ReadBytes('\n')

	profilePath := fmt.Sprintf("./vk_profile_%s", mode)

	ClearChromeLock(profilePath)

	// 1. Настраиваем Chrome в видимом режиме (Headless = false)
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", false), // Важно: показываем окно юзеру
		chromedp.Flag("disable-gpu", false),
		chromedp.Flag("user-data-dir", profilePath),
	)

	allocCtx, cancel_a := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel_a()

	ctx, cancel_c := chromedp.NewContext(allocCtx)
	defer cancel_c()

	// 2. Открываем ВК и ждем авторизации
	// var allCookies []*network.Cookie // Переменная для сохранения результата
	fmt.Println("Ожидание входа пользователя...")

	err := chromedp.Run(ctx,
		chromedp.Navigate("https://vk.com"),

		// Ждем, пока пользователь введет пароль и появится левое меню (признак входа)
		chromedp.WaitVisible(`#side_bar_inner`, chromedp.ByQuery),

		// // ИСПРАВЛЕНИЕ: Оборачиваем получение куков в ActionFunc
		// chromedp.ActionFunc(func(ctx context.Context) error {
		// 	var err error
		// 	// Выполняем команду .Do(ctx) и сохраняем результат во внешнюю переменную
		// 	allCookies, err = network.GetCookies().Do(ctx)
		// 	return err
		// }),
	)

	if err != nil {
		return fmt.Errorf("%s", err)
	}

	saveConfig(mode)

	fmt.Println("Нажмите Enter после входа, чтобы сохранить сессию...")
	bufio.NewReader(os.Stdin).ReadBytes('\n')

	return nil
}

func saveConfig(mode string) {
	config_name := fmt.Sprintf("config_%s.env", mode)
	// cookie_path := fmt.Sprintf("cookies_%s.json", mode)
	profile_path := fmt.Sprintf("./vk_profile_%s", mode)

	f, _ := os.Create(config_name)
	defer f.Close()

	// f.WriteString(fmt.Sprintf("VK_COOKIES_PATH=%s\n", cookie_path))
	f.WriteString(fmt.Sprintf("VK_PROFILE_PATH=%s\n", profile_path))
	f.WriteString(fmt.Sprintf("MODE=%s\n", mode))

	// data, err := json.MarshalIndent(cookies, "", "  ")
	// if err != nil {
	// 	return err
	// }

	// return os.WriteFile(cookie_path, data, 0600)
}

// LoadConfig загружает переменные из .env файла
func LoadConfig(mode string) (*Config, error) {
	config_name := fmt.Sprintf("config_%s.env", mode)

	if err := godotenv.Load(config_name); err != nil {
		return nil, fmt.Errorf("File with config %s not found.", config_name)
	}

	// vkCookiesPath := os.Getenv("VK_COOKIES_PATH")
	peerID := os.Getenv("VK_TARGET_SERVER_ID")
	vkProfilePath := os.Getenv("VK_PROFILE_PATH")
	mode_env := os.Getenv("MODE")

	if vkProfilePath == "" {
		return nil, fmt.Errorf("VK_COOKIES not found")
	}

	_, err := os.Stat(vkProfilePath)
	if err == nil {
		return &Config{
			// VkCookiesPath: vkCookiesPath,
			VkProfilePath: vkProfilePath,
			VkTargetID:    peerID,
			Mode:          mode_env,
		}, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s not exist", vkProfilePath)
	}
	return nil, err
}

// clearChromeLock удаляет файлы блокировки, которые мешают запуску Chrome
func ClearChromeLock(profilePath string) {
	// Chrome в Linux создает эти файлы как символические ссылки или сокеты
	lockFiles := []string{"SingletonLock", "SingletonSocket", "SingletonCookie"}

	for _, fileName := range lockFiles {
		fullPath := filepath.Join(profilePath, fileName)

		// Проверяем существование файла
		if _, err := os.Lstat(fullPath); err == nil {
			fmt.Printf("[Fix] Removing stale lock file: %s\n", fileName)
			err := os.Remove(fullPath)
			if err != nil {
				fmt.Printf("[Warning] Could not remove %s: %v\n", fileName, err)
			}
		}
	}
}
