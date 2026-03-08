package transport

import (
	"bytes"
	"context"
	"log"
	"net"
	"net/url"
	"time"

	// *> needed delete
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"

	// <* needed delete

	"gitlab.com/41f04d3bba15/obfs4webrtc/pkg/stego"

	"github.com/bluenviron/gortmplib"
	// "github.com/bluenviron/gortmplib/pkg/formats"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
)

const (
	rutubeServer  = "rtmp://rtmp-lb-a.dth.rutube.ru/live_push/"
	rutubeServer2 = "rtmp://rtmp-lb.ost.rutube.ru/live_push/"
)

// FakeVideoConn - это обертка над обычным net.Conn.
// Она реализует интерфейс net.Conn.
type FakeVideoConn_rutube struct {
	net.Conn
	// buffer нужен, если мы читаем заголовок "фейкового" видео,
	// но внутри него есть полезные данные, которые нужно склеить.
	buffer bytes.Buffer
}

// NewFakeVideoConn оборачивает "сырое" соединение
func NewFakeVideoConn_rutube(c net.Conn) *FakeVideoConn_rutube {
	return &FakeVideoConn_rutube{Conn: c}
}

// Write - внедряет маскировку перед отправкой данных
func (fc *FakeVideoConn_rutube) Write(b []byte) (n int, err error) {
	// ТУТ МАГИЯ:
	// Прежде чем отправить реальные данные 'b', мы можем
	// отправить фейковые HTTP заголовки, похожие на запрос сегмента видео.

	// Пример (очень грубый):
	// fakeHeader := []byte("POST /video/segment-123 HTTP/1.1\r\nHost: rutube.ru\r\n...")
	// fc.Conn.Write(fakeHeader)

	// Затем шифруем и отправляем реальные данные
	// encryptedData := Encrypt(b)
	// return fc.Conn.Write(encryptedData)

	return fc.Conn.Write(b) // Пока просто проксируем
}

// Read - очищает маскировку при получении
func (fc *FakeVideoConn_rutube) Read(b []byte) (n int, err error) {
	// ТУТ МАГИЯ:
	// Мы читаем данные из fc.Conn, отбрасываем фейковые заголовки "видео-сервиса"
	// и возвращаем в 'b' только чистую полезную нагрузку (payload).

	return fc.Conn.Read(b)
}

func multiplyAndDivide(v, m, d int64) int64 {
	secs := v / d
	dec := v % d
	return (secs*m + dec*m/d)
}

// ffmpeg -i music.mp3 -c:a aac output.flv
func RTMP_push_music(Rutube string) {

	// u, err := url.Parse(Rutube)
	// if err != nil {
	// 	panic(err)
	// }

	// if u.Port() == "" {
	// 	u.Host = net.JoinHostPort(u.Hostname(), "1935")
	// }

	// c := &gortmplib.Client{
	// 	URL:     u,
	// 	Publish: true,
	// }
	// err = c.Initialize(context.Background())
	// if err != nil {
	// 	panic(err)
	// }

	// track := &format.MPEG4Audio{
	// 	Config: &mpeg4audio.AudioSpecificConfig{
	// 		Type:         mpeg4audio.ObjectTypeAACLC,
	// 		SampleRate:   44100,
	// 		ChannelCount: 2,
	// 	},
	// }

	// c.NetConn().SetReadDeadline(time.Now().Add(10 * time.Second))

	// w := &gortmplib.Writer{
	// 	Conn:   c,
	// 	Tracks: []format.Format{track},
	// }
	// err = w.Initialize()
	// if err != nil {
	// 	panic(err)
	// }

	// // setup LPCM -> MPEG-4 Audio encoder
	// mp4aEnc := &mp4aEncoder{}
	// err = mp4aEnc.initialize()
	// if err != nil {
	// 	panic(err)
	// }
	// defer mp4aEnc.close()

	// start := time.Now()
	// prevPTS := int64(0)

	// // setup a ticker to sleep between writings
	// ticker := time.NewTicker(100 * time.Millisecond)
	// defer ticker.Stop()

	// c.NetConn().SetReadDeadline(time.Time{})

	// for range ticker.C {
	// 	// get current timestamp
	// 	pts := multiplyAndDivide(int64(time.Since(start)), int64(44100), int64(time.Second))

	// 	// generate dummy LPCM audio samples
	// 	samples := createDummyAudio(pts, prevPTS)

	// 	// encode samples with MPEG-4 Audio
	// 	aus, outPTS, err := mp4aEnc.encode(samples)
	// 	if err != nil {
	// 		panic(err)
	// 	}
	// 	if aus == nil {
	// 		continue
	// 	}

	// 	log.Printf("writing access units")

	// 	for _, au := range aus {
	// 		err = w.WriteMPEG4Audio(track, time.Duration(outPTS*int64(time.Second)/44100), au)
	// 		if err != nil {
	// 			panic(err)
	// 		}

	// 		outPTS += mpeg4audio.SamplesPerAccessUnit
	// 	}

	// 	prevPTS = pts
	// }

}

func RTMP_push_video(Rutube string) {

	u, err := url.Parse(rutubeServer + Rutube)
	if err != nil {
		panic(err)
	}

	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), "1935")
	}

	c := &gortmplib.Client{
		URL:     u,
		Publish: true,
	}

	err = c.Initialize(context.Background())
	if err != nil {
		panic(err)
	}

	track := &format.H264{}

	c.NetConn().SetReadDeadline(time.Now().Add(10 * time.Second))

	w := &gortmplib.Writer{
		Conn:   c,
		Tracks: []format.Format{track},
	}
	err = w.Initialize()
	if err != nil {
		panic(err)
	}

	// setup RGBA -> H264 encoder
	h264enc := &stego.H264Encoder{
		Width:  640,
		Height: 480,
		FPS:    5,
	}
	err = h264enc.Initialize()
	if err != nil {
		panic(err)
	}
	defer h264enc.Close()

	start := time.Now()

	// setup a ticker to sleep between frames
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	c.NetConn().SetReadDeadline(time.Time{})

	for range ticker.C {
		// get current timestamp
		pts := time.Since(start)

		// create a dummy image
		img := stego.CreateDummyImage()

		// encode the image with H264
		au, pts, err := h264enc.Encode(img, pts)
		if err != nil {
			panic(err)
		}

		// wait for a H264 access unit
		if au == nil {
			continue
		}

		// log.Printf("writing access unit")

		err = w.WriteH264(track, pts, pts, au)
		if err != nil {
			panic(err)
		}
	}
}

// RTMP_push_stream отправляет и ВИДЕО, и АУДИО в одном соединении
// func RTMP_push_stream(RutubeKey string) {
// 	// 1. Подготовка URL
// 	u, err := url.Parse(rutubeServer + RutubeKey)
// 	if err != nil {
// 		panic(err)
// 	}
// 	if u.Port() == "" {
// 		u.Host = net.JoinHostPort(u.Hostname(), "1935")
// 	}

// 	// 2. Создаем ОДИН клиент
// 	c := &gortmplib.Client{
// 		URL:     u,
// 		Publish: true,
// 	}

// 	err = c.Initialize(context.Background())
// 	if err != nil {
// 		panic(err)
// 	}
// 	defer c.Close()

// 	// 3. Создаем описания треков
// 	// Видео (H.264)
// 	videoTrack := &format.H264{
// 		PayloadTyp: 96,
// 		// SPS и PPS обычно заполняются энкодером автоматически,
// 		// но gortmplib может потребовать их наличие для отправки заголовка.
// 		// В вашем случае энкодер должен их отдать при инициализации.
// 	}

// 	// Аудио (AAC)
// 	audioTrack := &format.MPEG4Audio{
// 		PayloadTyp: 97,
// 		Config: &format.MPEG4AudioConfig{
// 			Type:         2, // AAC LC
// 			SampleRate:   48000,
// 			ChannelCount: 1, // Mono (как мы решили ранее)
// 		},
// 	}

// 	// 4. Создаем ОДИН Writer для обоих треков
// 	w := &gortmplib.Writer{
// 		Conn:   c,
// 		Tracks: []format.Format{videoTrack, audioTrack},
// 	}

// 	// Инициализация писателя отправит метаданные на сервер
// 	err = w.Initialize()
// 	if err != nil {
// 		panic(err)
// 	}

// 	// 5. Инициализация энкодеров
// 	// Видео
// 	h264enc := &stego.H264Encoder{
// 		Width:  640,
// 		Height: 480,
// 		FPS:    10, // Чуть поднял FPS для стабильности
// 	}
// 	if err := h264enc.Initialize(); err != nil {
// 		panic(err)
// 	}
// 	defer h264enc.Close()

// 	// Аудио
// 	mp4aEnc := &stego.MP4aEncoder{}
// 	if err := mp4aEnc.Initialize(); err != nil {
// 		panic(err)
// 	}
// 	defer mp4aEnc.Close()

// 	// 6. Главный цикл отправки
// 	// Нам нужны разные таймеры для видео и аудио, так как частота разная.

// 	// Видео таймер (например, 10 FPS = 100ms)
// 	videoTicker := time.NewTicker(100 * time.Millisecond)
// 	defer videoTicker.Stop()

// 	// Аудио таймер.
// 	// AAC фрейм обычно содержит 1024 сэмпла.
// 	// При 48000Hz: 1024 / 48000 ≈ 21.3 мс.
// 	audioTicker := time.NewTicker(21 * time.Millisecond)
// 	defer audioTicker.Stop()

// 	start := time.Now()

// 	// Снимаем дедлайн для потоковой передачи
// 	c.NetConn().SetReadDeadline(time.Time{})

// 	log.Println("Начинаем стриминг...")

// 	for {
// 		select {
// 		case <-videoTicker.C:
// 			// --- ОБРАБОТКА ВИДЕО ---
// 			pts := time.Since(start)

// 			// Сюда будете вшивать данные WireGuard
// 			img := stego.CreateDummyImage()

// 			au, _, err := h264enc.encode(img, pts) // pts пересчитывайте внутри как вам удобно
// 			if err != nil {
// 				log.Println("Video encode error:", err)
// 				return
// 			}
// 			if au == nil {
// 				continue
// 			}

// 			// Пишем видео в трек videoTrack
// 			if err := w.WriteH264(videoTrack, pts, pts, au); err != nil {
// 				log.Println("WriteH264 error:", err)
// 				return
// 			}

// 		case <-audioTicker.C:
// 			// --- ОБРАБОТКА АУДИО ---
// 			// Можно использовать простой счетчик для PTS аудио или вычислять от start

// 			// Генерируем "тишину" или аудио-шум (если данные пойдут через аудио)
// 			samples := stego.CreateDummyAudio(1024) // 1024 сэмпла

// 			aus, _, err := mp4aEnc.Encode(samples)
// 			if err != nil {
// 				log.Println("Audio encode error:", err)
// 				return
// 			}
// 			if aus == nil {
// 				continue
// 			}

// 			// Вычисляем PTS для аудио (лучше считать точно по количеству сэмплов)
// 			// Здесь упрощенно:
// 			audioPTS := time.Since(start)

// 			for _, au := range aus {
// 				// Пишем аудио в трек audioTrack
// 				if err := w.WriteMPEG4Audio(audioTrack, audioPTS, au); err != nil {
// 					log.Println("WriteMPEG4Audio error:", err)
// 					return
// 				}
// 			}
// 		}
// 	}
// }

func RTMP_pull_stream(Rutube string) {
	log.SetFlags(log.Ltime | log.Lshortfile)
	log.Println("Запуск Rutube клиента v2 (Enhanced)...")

	videoID, err := getVideoID(Rutube)
	if err != nil {
		log.Fatal("Needed link https://rutube.ru/video/private/.../?p=...")
		return
	}
	// 2. Ищем ссылку m3u8 (Универсальный поиск)
	masterURL, err := findM3U8Universal(videoID)
	if err != nil {
		log.Fatalf("ОШИБКА: %v", err)
	}
	log.Println("-> Найдена ссылка:", masterURL)

	// 3. Выбираем лучший поток
	mediaPlaylistURL, err := getBestStreamVariant(masterURL)
	if err != nil {
		// Иногда masterURL сам является потоком, пробуем его
		log.Println("Не удалось найти варианты качества, пробуем использовать мастер-ссылку напрямую.")
		mediaPlaylistURL = masterURL
	}
	log.Println("-> Выбран поток:", mediaPlaylistURL)

	// 4. Скачиваем
	outFile, err := os.Create("video_dump.ts")
	if err != nil {
		log.Fatalf("Ошибка создания файла: %v", err)
	}
	defer outFile.Close()

	log.Println("ЗАПИСЬ... (Нажмите Ctrl+C для остановки)")
	err = captureStream(mediaPlaylistURL, outFile)
	if err != nil {
		log.Fatalf("Стрим прервался: %v", err)
	}
}

// findM3U8Universal скачивает JSON и ищет любую строку с ".m3u8" внутри
func findM3U8Universal(id string) (string, error) {
	// Добавляем pver (версию плеера), часто это требуется для генерации ссылок
	apiURL := fmt.Sprintf("https://rutube.ru/api/play/options/%s/?no_404=true&referer=rutube.ru&pver=v2", id)

	req, _ := http.NewRequest("GET", apiURL, nil)
	// Притворяемся плеером, встроенным на сайт
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Referer", fmt.Sprintf("https://rutube.ru/play/embed/%s", id))

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)

	// Выводим JSON для отладки (первые 500 символов, чтобы не засорять)
	debugJSON := string(bodyBytes)
	if len(debugJSON) > 500 {
		debugJSON = debugJSON[:500] + "..."
	}
	log.Printf("Ответ API (фрагмент): %s", debugJSON)

	// Парсим в произвольную структуру
	var result interface{}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return "", fmt.Errorf("invalid JSON: %v", err)
	}

	// Рекурсивный поиск ссылки
	foundURL := recursiveSearch(result)
	if foundURL == "" {
		return "", fmt.Errorf("ссылка .m3u8 не найдена ни в одном поле JSON")
	}

	return foundURL, nil
}

// recursiveSearch бегает по всему JSON дереву
func recursiveSearch(data interface{}) string {
	switch v := data.(type) {
	case string:
		if strings.Contains(v, ".m3u8") && strings.HasPrefix(v, "http") {
			return v
		}
	case map[string]interface{}:
		for _, val := range v {
			res := recursiveSearch(val)
			if res != "" {
				return res
			}
		}
	case []interface{}:
		for _, val := range v {
			res := recursiveSearch(val)
			if res != "" {
				return res
			}
		}
	}
	return ""
}

// --- Остальные функции те же самые ---

func extractID(rawURL string) string {
	parts := strings.Split(strings.TrimRight(rawURL, "/"), "/")
	return parts[len(parts)-1]
}

func getBestStreamVariant(masterURL string) (string, error) {
	content, err := fetchString(masterURL)
	if err != nil {
		return "", err
	}
	// Если внутри #EXTINF, значит это уже чанклист
	if strings.Contains(content, "#EXTINF") {
		return masterURL, nil // Возвращаем исходную ссылку, это не мастер-лист
	}

	var bestURL string
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			bestURL = line
		}
	}
	if bestURL == "" {
		return "", fmt.Errorf("нет вариантов")
	}
	return resolveURL(masterURL, bestURL)
}

func captureStream(playlistURL string, output io.Writer) error {
	processedSegments := make(map[string]bool)
	client := &http.Client{Timeout: 15 * time.Second}

	for {
		playlistContent, err := fetchString(playlistURL)
		if err != nil {
			log.Printf("Warn: Ошибка плейлиста %v", err)
			time.Sleep(2 * time.Second)
			continue
		}

		lines := strings.Split(playlistContent, "\n")
		var newSegments []string
		isVOD := strings.Contains(playlistContent, "#EXT-X-ENDLIST")

		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				segURL, _ := resolveURL(playlistURL, line)
				if !processedSegments[segURL] {
					newSegments = append(newSegments, segURL)
					processedSegments[segURL] = true
				}
			}
		}

		if len(newSegments) > 0 {
			log.Printf("Загрузка %d сегментов...", len(newSegments))
			for _, segURL := range newSegments {
				resp, err := client.Get(segURL)
				if err != nil {
					log.Printf("Error loading seg: %v", err)
					continue
				}
				data, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				output.Write(data)
			}
		} else if isVOD {
			log.Println("Все сегменты загружены. Готово.")
			return nil
		}

		time.Sleep(2 * time.Second)
	}
}

func fetchString(url string) (string, error) {
	req, _ := http.NewRequest("GET", url, nil)
	// Важно: некоторые CDN проверяют User-Agent
	req.Header.Set("User-Agent", "Mozilla/5.0")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	bytes, err := io.ReadAll(resp.Body)
	return string(bytes), err
}

func resolveURL(base, relative string) (string, error) {
	if strings.HasPrefix(relative, "http") {
		return relative, nil
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	rel, err := url.Parse(relative)
	if err != nil {
		return "", err
	}
	return u.ResolveReference(rel).String(), nil
}

func getVideoID(url string) (string, error) {
	// re := regexp.MustCompile(`rutube\.ru/video/(?:private/)?([0-9a-fA-F]{32})(?:/|\?)?`)
	re := regexp.MustCompile(`rutube\.ru/video/(?:private/)?([0-9a-fA-F]{32}.*)`)

	m := re.FindStringSubmatch(url)
	if len(m) > 1 {
		// fmt.Println("ID:", m[1])
		return m[1], nil
	}
	return "", errors.New("video ID not found")
}
