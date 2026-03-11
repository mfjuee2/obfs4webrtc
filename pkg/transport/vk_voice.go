package transport

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"

	"github.com/chromedp/chromedp"
	"github.com/pion/webrtc/v3"
	"github.com/songgao/water"
	"gitlab.com/41f04d3bba15/obfs4webrtc/internal/config"
)

// --- Глобальные переменные Pion ---
var (
	pionPC      *webrtc.PeerConnection
	pionDC      *webrtc.DataChannel
	tunDev      *water.Interface // Вместо wgConn
	mu          sync.Mutex
	cleanupMu   sync.Mutex
	cleanupCmds [][]string // Стек команд для выполнения при выходе
)

// --- JS INJECTION (JS Signaling Interceptor + DEBUG) ---
const bridgeJS = `
(function() {
    if (window._pionInjected) return;
    window._pionInjected = true;
    console.log("[PION] Injection v7 (Double Agent Mode)...");

    window.callPion = async function(method, ...args) {
        return new Promise(async resolve => {
            const id = Math.round(Math.random() * 100000);
            window._pionResolvers = window._pionResolvers || {};
            window._pionResolvers[id] = resolve;
            if (window[method]) {
                try {
                    window[method](JSON.stringify({id: id, args: args}));
                } catch (e) { resolve(null); }
            } else { resolve(null); }
            setTimeout(() => {
                if(window._pionResolvers[id]) {
                    delete window._pionResolvers[id];
                    resolve(null);
                }
            }, 10000);
        });
    };

    const wrapPC = (originalPC) => {
        const NewPC = function(config) {
            const pc = new originalPC(config);
            let isPionControlled = false;
            let pionSDP = null;
            let nativeSDP = null;

            const getCredentials = (cfg) => {
                const ice = cfg.iceServers || [];
                const turn = ice.find(s => s.username && s.credential);
                return { 
                    urls: ice.flatMap(s => s.urls || []), 
                    user: turn ? turn.username : "", 
                    cred: turn ? turn.credential : "" 
                };
            };

            // --- 1. CREATE OFFER (Client) ---
            const origCreateOffer = pc.createOffer;
            pc.createOffer = async function() {
                console.log("[JS] createOffer detected");
                nativeSDP = await origCreateOffer.apply(this, arguments);
                const { urls, user, cred } = getCredentials(this.getConfiguration());
                
                const b64 = await window.callPion('initPionAsClient', urls, user, cred);
                if (b64) {
                    isPionControlled = true;
                    const obj = JSON.parse(atob(b64));
                    pionSDP = { type: 'offer', sdp: obj.sdp };
                    console.log("[JS] Pion Offer ready. SDP Offer Modification...");
                    return pionSDP; 
                }
                return nativeSDP;
            };

            // --- 2. CREATE ANSWER (Server) ---
            const origCreateAnswer = pc.createAnswer;
            pc.createAnswer = async function() {
                console.log("[JS] createAnswer detected");
                nativeSDP = await origCreateAnswer.apply(this, arguments);
                if (isPionControlled) {
                    const b64 = await window.callPion('getPionAnswer');
                    if (b64) {
                        const obj = JSON.parse(atob(b64));
                        pionSDP = { type: 'answer', sdp: obj.sdp };
                        console.log("[JS] Pion Answer ready.");
                        return pionSDP;
                    }
                }
                return nativeSDP;
            };

            // --- 3. SET LOCAL (The Trick) ---
            const origSetLocal = pc.setLocalDescription;
            pc.setLocalDescription = async function(desc) {
                console.log("[JS] setLocalDescription called");
                // Если сайт пытается поставить Pion SDP, мы подсовываем браузеру нативный
                if (isPionControlled && pionSDP && desc.sdp === pionSDP.sdp) {
                    return await origSetLocal.apply(this, [nativeSDP]);
                }
                return await origSetLocal.apply(this, arguments);
            };

            // --- 4. GET LOCAL (The Deception) ---
            Object.defineProperty(pc, 'localDescription', {
                get: function() {
                    const real = Object.getOwnPropertyDescriptor(originalPC.prototype, 'localDescription').get.call(this);
                    if (isPionControlled && pionSDP) return pionSDP;
                    return real;
                }
            });

            // --- 5. SET REMOTE ---
            const origSetRemote = pc.setRemoteDescription;
            pc.setRemoteDescription = async function(desc) {
                console.log("[JS] setRemoteDescription type: " + desc.type);
                const { urls, user, cred } = getCredentials(this.getConfiguration());

                if (desc.type === 'offer') {
                    isPionControlled = true;
                    await window.callPion('initPionAsServer', urls, user, cred, btoa(JSON.stringify(desc)));
                } else if (desc.type === 'answer' && isPionControlled) {
                    await window.callPion('passAnswerToPion', btoa(JSON.stringify(desc)));
                }
                return await origSetRemote.apply(this, arguments);
            };

            return pc;
        };
        NewPC.prototype = originalPC.prototype;
        return NewPC;
    };

    window.RTCPeerConnection = wrapPC(window.RTCPeerConnection);
    if (window.webkitRTCPeerConnection) window.webkitRTCPeerConnection = wrapPC(window.webkitRTCPeerConnection);
})();
`

// --- GO LOGIC ---

func setupPion(urls []string, user, cred string) error {
	if pionPC != nil {
		pionPC.Close()
		pionPC = nil
	}

	var iceServers []webrtc.ICEServer

	if user != "" && cred != "" {
		fmt.Println("[GO] Setting up TURN with VK credentials!")

		var turnUrls []string
		for _, u := range urls {
			// Берем только TURN
			if len(u) > 4 && u[:4] == "turn" {
				turnUrls = append(turnUrls, u)
			}
		}

		// Если вдруг список пуст, но креды есть (редкий баг парсинга), выведем варнинг
		if len(turnUrls) == 0 {
			fmt.Println("[GO] Warning: Credentials present but no TURN URLs parsed.")
		}

		iceServers = append(iceServers, webrtc.ICEServer{
			URLs:           turnUrls,
			Username:       user,
			Credential:     cred,
			CredentialType: webrtc.ICECredentialTypePassword,
		})
	} else {
		fmt.Println("[GO] No VK credentials found. FALLBACK STUN.")
		iceServers = append(iceServers, webrtc.ICEServer{URLs: []string{"stun:stun.l.google.com:19302"}})
	}

	config := webrtc.Configuration{
		ICEServers: iceServers,
		// Оставляем Relay, так как это цель обфускации.
		// Если не работает - значит проблема в доступности TURN портов с машины.
		ICETransportPolicy: webrtc.ICETransportPolicyRelay,
	}

	var err error
	pionPC, err = webrtc.NewPeerConnection(config)
	if err != nil {
		return fmt.Errorf("failed to create PC: %w", err)
	}

	// Логируем смену состояний
	pionPC.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		fmt.Printf("[PION ICE] %s\n", s.String())
	})

	// Добавляем Audio Transceiver, чтобы VK думал что это звонок
	_, err = pionPC.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{
		Direction: webrtc.RTPTransceiverDirectionSendrecv,
	})

	return err
}

func setupDataChannel(dc *webrtc.DataChannel) {
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		if tunDev == nil {
			return
		}
		// Пишем сырой пакет обратно в ядро Linux
		_, err := tunDev.Write(msg.Data)
		if err != nil {
			log.Println("Write to TUN error:", err)
		}
	})
}

// Вызывается из JS
func InitPionAsClient(iceUrls []string, user, cred string) string {
	mu.Lock()
	defer mu.Unlock()
	fmt.Println("[GO] InitPionAsClient started...")

	// --- АВТО-РОУТИНГ ---
	// Сначала исключаем VK и TURN серверы из VPN
	setupBypassRoutes(iceUrls)

	if err := setupPion(iceUrls, user, cred); err != nil {
		fmt.Println("setupPion Error:", err)
		return ""
	}

	pionDC, _ = pionPC.CreateDataChannel("wg-tunnel", nil)
	// ВАЖНО: Тут мы начинаем читать из TUN
	setupDataChannel(pionDC)

	offer, err := pionPC.CreateOffer(nil)
	if err != nil {
		fmt.Println("CreateOffer Error:", err)
		return ""
	}
	pionPC.SetLocalDescription(offer)

	// ВАЖНО: Ждем сбора кандидатов, но с таймаутом!
	// Если сеть заблочена или STUN недоступен, GatheringCompletePromise может висеть вечно.
	fmt.Println("[GO] Gathering candidates...")
	select {
	case <-webrtc.GatheringCompletePromise(pionPC):
		fmt.Println("[GO] Gathering Complete.")
	case <-time.After(2 * time.Second):
		fmt.Println("[GO] Gathering Timed Out (sending what we have).")
	}

	desc := pionPC.LocalDescription()
	bytes, _ := json.Marshal(*desc)
	b64 := base64.StdEncoding.EncodeToString(bytes)
	fmt.Printf("[GO] Offer ready (%d chars)\n", len(b64))

	// ПОСЛЕ того как оффер создан и мы готовы - включаем глобальный роутинг
	// Делаем это в горутине с небольшой задержкой, чтобы WebRTC успел схватиться
	go func() {
		time.Sleep(3 * time.Second)
		enableGlobalTunRouting() // client is false
	}()

	return b64
}

func InitPionAsServer(iceUrls []string, user, cred, b64Offer string) {
	mu.Lock()
	defer mu.Unlock()
	fmt.Println("[GO] InitPionAsServer called.")

	// --- АВТО-РОУТИНГ ---
	// Сначала исключаем VK и TURN серверы из VPN
	setupBypassRoutes(iceUrls)

	setupPion(iceUrls, user, cred)

	var offer webrtc.SessionDescription
	b, _ := base64.StdEncoding.DecodeString(b64Offer)
	json.Unmarshal(b, &offer)

	pionPC.OnDataChannel(func(d *webrtc.DataChannel) {
		fmt.Println("[GO] Received DataChannel from Client!")
		pionDC = d
		setupDataChannel(d)
	})

	err := pionPC.SetRemoteDescription(offer)
	if err != nil {
		fmt.Println("SetRemoteDescription Error:", err)
	} else {
		fmt.Println("[GO] Remote Description Set.")
	}
}

func GetPionAnswer() string {
	mu.Lock() // Блокировка, так как PC общий
	defer mu.Unlock()

	if pionPC == nil {
		return ""
	}

	answer, err := pionPC.CreateAnswer(nil)
	if err != nil {
		fmt.Println("CreateAnswer Error:", err)
		return ""
	}
	pionPC.SetLocalDescription(answer)

	fmt.Println("[GO] Gathering candidates for Answer...")
	select {
	case <-webrtc.GatheringCompletePromise(pionPC):
		fmt.Println("[GO] Gathering Complete.")
	case <-time.After(2 * time.Second):
		fmt.Println("[GO] Gathering Timed Out.")
	}

	bytes, _ := json.Marshal(*pionPC.LocalDescription())
	return base64.StdEncoding.EncodeToString(bytes)
}

func PassAnswerToPion(b64Answer string) {
	mu.Lock()
	defer mu.Unlock()

	if pionPC == nil {
		return
	}
	var answer webrtc.SessionDescription
	b, _ := base64.StdEncoding.DecodeString(b64Answer)
	json.Unmarshal(b, &answer)

	if err := pionPC.SetRemoteDescription(answer); err != nil {
		fmt.Println("SetRemote (Answer) Error:", err)
	} else {
		fmt.Println("[GO] Remote Answer Set. Waiting for connection...")
	}
}

// --- NETWORK ---

func pumpTun() {
	packet := make([]byte, 2048)
	for {
		n, err := tunDev.Read(packet)
		if err != nil {
			log.Println("Read form TUN error:", err)
			continue
		}

		// Если канал WebRTC готов, шлем пакет
		if pionDC != nil && pionDC.ReadyState() == webrtc.DataChannelStateOpen {
			// WebRTC сам разобьет на фрагменты если надо, но лучше соблюдать MTU
			err := pionDC.Send(packet[:n])
			if err != nil {
				log.Println("WebRTC Send error:", err)
			}
		}
	}
}

// Настройка TUN интерфейса
func setupTun(cidr string, isServer bool) error {
	config := water.Config{
		DeviceType: water.TUN,
	}
	config.Name = "obfsvpn"

	var err error
	tunDev, err = water.New(config)
	if err != nil {
		return fmt.Errorf("error creating TUN: %w", err)
	}

	log.Printf("Interface %s created. Setting IP %s...", tunDev.Name(), cidr)

	// Поднимаем интерфейс
	exec.Command("ip", "link", "set", "dev", tunDev.Name(), "mtu", "1300", "up").Run()
	exec.Command("ip", "addr", "add", cidr, "dev", tunDev.Name()).Run()

	// РЕГИСТРИРУЕМ ОЧИСТКУ: Удаление интерфейса (хотя он сам удалится при закрытии проги, но для надежности)
	// registerCleanup([]string{"ip", "link", "delete", tunDev.Name()})
	// ^ Обычно TUN удаляется сам при закрытии дескриптора, важнее маршруты.

	// === ЛОГИКА СЕРВЕРА (NAT) ===
	// === ЛОГИКА СЕРВЕРА (NAT) ===
	if isServer {
		fmt.Println("[SERVER] Enabling NAT and IP Forwarding...")

		// 1. ЖЕСТКО включаем ip_forward (важно!)
		cmd := exec.Command("sh", "-c", "echo 1 > /proc/sys/net/ipv4/ip_forward")
		if err := cmd.Run(); err != nil {
			log.Printf("[ERROR] Failed to enable ip_forward: %v", err)
		}

		// 2. Определяем внешний интерфейс
		gwParams, err := getDefaultGatewayParams()
		if err == nil {
			iface := gwParams.Dev
			fmt.Printf("[SERVER] Outbound interface: %s\n", iface)

			// 3. Очистка старых правил (чтобы не дублировать при перезапусках)
			exec.Command("iptables", "-t", "nat", "-F").Run()
			exec.Command("iptables", "-P", "FORWARD", "ACCEPT").Run() // Важно: разрешить форвардинг по дефолту

			// 4. Добавляем правило MASQUERADE
			// Используем -A (Append) в чистую таблицу
			err := exec.Command("iptables", "-t", "nat", "-A", "POSTROUTING", "-s", "10.10.0.0/24", "-o", iface, "-j", "MASQUERADE").Run()
			if err != nil {
				log.Printf("iptables NAT error: %v", err)
			}
			registerCleanup([]string{"iptables", "-t", "nat", "-D", "POSTROUTING", "-s", "10.10.0.0/24", "-o", iface, "-j", "MASQUERADE"})

			// 5. ЯВНО разрешаем хождение пакетов между интерфейсами (bypass FORWARD DROP policy)
			// Разрешаем от tun к инету
			exec.Command("iptables", "-A", "FORWARD", "-i", tunDev.Name(), "-o", iface, "-j", "ACCEPT").Run()
			// Разрешаем от инета к tun (ответы)
			exec.Command("iptables", "-A", "FORWARD", "-i", iface, "-o", tunDev.Name(), "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT").Run()

			registerCleanup([]string{"iptables", "-D", "FORWARD", "-i", tunDev.Name(), "-o", iface, "-j", "ACCEPT"})
		} else {
			log.Printf("[SERVER ERROR] Could not detect gateway: %v", err)
		}
	}

	return nil
}

func RunBot(cfg *config.Config, targetId string) {
	// ClearChromeLock(cfg.VkProfilePath)

	// --- ПЕРЕХВАТ СИГНАЛОВ ---
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		PerformCleanup()
		os.Exit(0)
	}()

	// 1. Поднимаем интерфейс
	var cidr string
	isServer := (cfg.Mode == "server")

	if isServer {
		cidr = "10.10.0.1/24"
	} else {
		cidr = "10.10.0.2/24"
	}
	err := setupTun(cidr, isServer) // Для клиента. Для сервера "10.10.0.1/24"
	if err != nil {
		log.Printf("setupTun: %s", err)
		return
	}

	// 2. ЗАПУСКАЕМ ЧТЕНИЕ ИЗ TUN В ФОНЕ
	go pumpTun()

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.NoSandbox,
		chromedp.Flag("headless", true), // ВКЛЮЧИ ГОЛОВУ (HEADLESS=FALSE) ДЛЯ ТЕСТА!
		chromedp.Flag("disable-dev-shm-usage", true),
		// chromedp.Flag("remote-debugging-port", "9222"),
		chromedp.Flag("user-data-dir", cfg.VkProfilePath),
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"),
		chromedp.Flag("autoplay-policy", "no-user-gesture-required"),
		chromedp.Flag("use-fake-ui-for-media-stream", true),
		chromedp.Flag("use-fake-device-for-media-stream", true),
	)

	allocCtx, cancel_a := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel_a()

	// Создаем контекст БЕЗ отмены сразу
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	// Включаем автоматический захват новых окон/фреймов
	chromedp.ListenTarget(ctx, func(ev interface{}) {
		switch ev := ev.(type) {
		case *target.EventTargetCreated:
			// Если открылось новое окно или фрейм - прикрепляемся к нему
			go func() {
				tctx, _ := chromedp.NewContext(ctx, chromedp.WithTargetID(ev.TargetInfo.TargetID))
				if err := chromedp.Run(tctx); err != nil {
					return
				}
			}()
		case *runtime.EventConsoleAPICalled:
			for _, arg := range ev.Args {
				fmt.Printf("[BROWSER LOG] %s\n", arg.Value)
			}
		case *runtime.EventBindingCalled:
			// ЛОГ ДЛЯ ОТЛАДКИ: Видит ли Go хоть что-то?
			fmt.Printf("[DEBUG] Binding Called: %s | Payload: %s\n", ev.Name, ev.Payload)

			var payload struct {
				ID   int           `json:"id"`
				Args []interface{} `json:"args"`
			}
			if err := json.Unmarshal([]byte(ev.Payload), &payload); err != nil {
				fmt.Println("Payload Error:", err)
				return
			}

			// Обработка в горутине
			go handleBinding(ctx, ev.Name, payload.ID, payload.Args)
		}
	})

	// Инициализация биндингов ДО навигации
	err = chromedp.Run(ctx,
		runtime.AddBinding("initPionAsClient"),
		runtime.AddBinding("initPionAsServer"),
		runtime.AddBinding("getPionAnswer"),
		runtime.AddBinding("passAnswerToPion"),
		// Оборачиваем в ActionFunc, так как метод возвращает ID скрипта, а Run ждет только ошибку
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(bridgeJS).Do(ctx)
			return err
		}),
	)
	if err != nil {
		log.Fatal(err)
	}

	if targetId != "" {
		runClientLogic(ctx, targetId)
	} else {
		runServerLogic(ctx)
	}
}

// Вынесем обработку в отдельную функцию для чистоты
func handleBinding(ctx context.Context, name string, id int, args []interface{}) {
	var res string
	switch name {
	case "initPionAsClient":
		res = InitPionAsClient(parseUrls(args[0]), fmt.Sprint(args[1]), fmt.Sprint(args[2]))
	case "initPionAsServer":
		InitPionAsServer(parseUrls(args[0]), fmt.Sprint(args[1]), fmt.Sprint(args[2]), fmt.Sprint(args[3]))
		res = "ok"
	case "getPionAnswer":
		res = GetPionAnswer()
	case "passAnswerToPion":
		PassAnswerToPion(fmt.Sprint(args[0]))
		res = "ok"
	}

	// ВАЖНО: Используем Evaluate, который возвращает результат в ту же среду, где был вызван
	script := fmt.Sprintf(`
        if(window._pionResolvers && window._pionResolvers[%d]) { 
            window._pionResolvers[%d]("%s"); 
            delete window._pionResolvers[%d]; 
        }
    `, id, id, res, id)

	// Мы выполняем это в контексте, который пришел из ListenTarget
	// Если это сложно пробросить, можно просто использовать chromedp.Evaluate напрямую
	err := chromedp.Run(ctx, chromedp.Evaluate(script, nil))
	if err != nil {
		fmt.Printf("[GO] Error returning result to JS: %v\n", err)
	}
}

func parseUrls(in interface{}) []string {
	raw, _ := in.([]interface{})
	out := make([]string, len(raw))
	for i, v := range raw {
		out[i] = fmt.Sprint(v)
	}
	return out
}

// ... остальной код runClientLogic и runServerLogic оставь без изменений ...
// Только проверь, что в ClientLogic ты вызываешь chromedp.Sleep после клика,
// чтобы браузер успел обработать события.

func runClientLogic(ctx context.Context, targetId string) {
	url := fmt.Sprintf("https://vk.com/im?sel=%s", targetId)
	fmt.Println("[CLIENT] Starting a call...")

	err := chromedp.Run(ctx,
		network.Enable(),
		chromedp.Navigate(url),
		// 1. Ждем появления кнопки-трубки по её ID
		chromedp.WaitVisible(`#convo-call-menu-trigger`, chromedp.ByID),
		// 2. Кликаем по трубке, чтобы открыть меню
		chromedp.Click(`#convo-call-menu-trigger`, chromedp.ByID),
		// 3. Ждем, пока меню откроется (можно по классу или просто паузу)
		chromedp.Sleep(500*time.Millisecond),
		// 4. Кликаем на "Голосовой звонок" (Voice Call)
		// Ищем кнопку внутри открывшегося меню по тексту или aria-label
		chromedp.Evaluate(`
            (function() {
                // Ищем все элементы в меню звонков
                const menu = document.querySelector('#convo-call-menu') || document.body;
                const items = Array.from(menu.querySelectorAll('button, [role="menuitem"]'));

                // Ищем пункт "Голосовой звонок" или "Voice call"
                const voiceCallBtn = items.find(el => 
                    el.innerText.includes('Voice call') || 
                    el.innerText.includes('Голосовой звонок') ||
                    el.getAttribute('aria-label') === 'Voice call'
                );

                if (voiceCallBtn) {
                    voiceCallBtn.click();
                    // Мутируем все аудио-треки, чтобы браузер перестал активно "слушать"
                    navigator.mediaDevices.getUserMedia({audio: true}).then(stream => {
                        stream.getAudioTracks().forEach(track => track.enabled = false);
                    });
                    return "Call initiated";
                }
                return "Button Voice Call not found in menu";
            })()
        `, nil),

		chromedp.Sleep(5*time.Second), // Даем время на начало запросов

	)
	if err != nil {
		log.Fatal(err)
	}
	select {}
}

func runServerLogic(ctx context.Context) {
	fmt.Println("[SERVER] Started. Monitoring for calls and frames...")

	err := chromedp.Run(ctx,
		browser.SetPermission(
			&browser.PermissionDescriptor{Name: "microphone"},
			browser.PermissionSettingGranted,
		),
		chromedp.Navigate("https://vk.com/im"),
		// Улучшенный скрипт авто-ответа
		chromedp.Evaluate(`
            setInterval(() => {
                // Ищем все подозрительные кнопки
                const buttons = Array.from(document.querySelectorAll('button, [role="button"], div[clickable], .vkuiButton'));
                const acceptBtn = buttons.find(b => {
                    const text = (b.innerText || b.getAttribute('aria-label') || "").toLowerCase();
                    const hasIcon = b.querySelector('.vkuiIcon--phone_outline_24') || b.innerHTML.includes('phone_outline');
                    
                    return (text.includes('принять') || 
                            text.includes('answer') || 
                            hasIcon || 
                            b.classList.contains('Calls_Incall_accept'));
                });

                if (acceptBtn && acceptBtn.offsetParent !== null) {
                    console.log("[JS] INCOMING CALL! Clicking Accept...");
                    
                    // Эмуляция полного цикла нажатия
                    const events = ['mousedown', 'mouseup', 'click'];
                    events.forEach(name => {
                        acceptBtn.dispatchEvent(new MouseEvent(name, {
                            bubbles: true,
                            cancelable: true,
                            view: window
                        }));
                    });
                }
            }, 1000);
        `, nil),
	)
	if err != nil {
		log.Fatal(err)
	}
	select {}
}

// --- ROUTING HELPERS ---

// Получаем IP шлюза по умолчанию (маршрутизатора)
func getDefaultGateway() (string, error) {
	// Выполняем `ip route show default`
	out, err := exec.Command("ip", "route", "show", "default").Output()
	if err != nil {
		return "", err
	}
	// Строка вида: "default via 192.168.1.1 dev eth0 ..."
	parts := strings.Fields(string(out))
	if len(parts) > 2 && parts[0] == "default" && parts[1] == "via" {
		return parts[2], nil
	}
	return "", fmt.Errorf("gateway not found in ip route")
}

// Добавляет прямой маршрут до IP через шлюз провайдера
func addDirectRoute(ip string, gateway string) {
	// Проверяем, не является ли IP локальным, чтобы не ломать LAN
	if strings.HasPrefix(ip, "192.168.") || strings.HasPrefix(ip, "10.") || strings.HasPrefix(ip, "127.") {
		return
	}

	// Добавляем маршрут
	err := exec.Command("ip", "route", "add", ip+"/32", "via", gateway).Run()
	if err == nil {
		// Если успешно добавили, регистрируем удаление
		registerCleanup([]string{"ip", "route", "del", ip + "/32"})
		fmt.Printf("[ROUTING] Added bypass: %s via %s\n", ip, gateway)
	}
}

// Разрешает домен в IP и добавляет маршрут
func bypassDomain(domain string, gateway string) {
	// Убираем порт если есть (stun.l.google.com:19302 -> stun.l.google.com)
	if strings.Contains(domain, ":") {
		host, _, err := net.SplitHostPort(domain)
		if err == nil {
			domain = host
		}
	}

	ips, err := net.LookupIP(domain)
	if err != nil {
		fmt.Printf("[ROUTING] Failed to resolve %s: %v\n", domain, err)
		return
	}
	for _, ip := range ips {
		if ip.To4() != nil { // Нам нужны только IPv4
			addDirectRoute(ip.String(), gateway)
		}
	}
}

// Главная функция настройки исключений
func setupBypassRoutes(iceUrls []string) {
	gw, err := getDefaultGateway()
	if err != nil {
		fmt.Println("[ROUTING] Critical: Could not find Default Gateway!", err)
		return
	}
	fmt.Printf("[ROUTING] Default Gateway detected: %s\n", gw)

	// 1. Статический список доменов VK (основные узлы)
	vkDomains := []string{
		"vk.com", "www.vk.com", "im.vk.com", "login.vk.com", "api.vk.com",
		"srv.vk.com", "pu.vk.com", "sun9-1.userapi.com", // CDN и прочее
	}

	for _, d := range vkDomains {
		bypassDomain(d, gw)
	}

	// 2. Динамический список из ICE серверов (которые пришли из JS)
	for _, u := range iceUrls {
		// url вида "turn:95.142.192.10:3478?transport=udp" или "stun:stun.l.google.com:19302"
		// Нам нужно вытащить хост
		parts := strings.Split(u, ":")
		if len(parts) >= 2 {
			// parts[0] = scheme, parts[1] = host (или host:port)
			// Это грубый парсинг, но для WebRTC строк обычно работает
			// Чистим от лишнего
			// port := parts[1]
			// if len(port) > 2 {
			// 	// Если есть порт, то parts[1] может быть чистым хостом, проверять надо аккуратно
			// 	// Проще использовать strings.TrimPrefix
			// }

			// Более надежный парсинг URL
			cleanUrl := strings.TrimPrefix(u, "turn:")
			cleanUrl = strings.TrimPrefix(cleanUrl, "stun:")
			cleanUrl = strings.TrimPrefix(cleanUrl, "turntcp:")

			// Если там есть порт или query params
			if idx := strings.Index(cleanUrl, "?"); idx != -1 {
				cleanUrl = cleanUrl[:idx]
			}

			bypassDomain(cleanUrl, gw)
		}
	}
}

// Добавляем функцию регистрации очистки (LIFO - удаляем в обратном порядке)
func registerCleanup(cmd []string) {
	cleanupMu.Lock()
	defer cleanupMu.Unlock()
	cleanupCmds = append(cleanupCmds, cmd)
}

// Функция запуска очистки
func PerformCleanup() {
	cleanupMu.Lock()
	defer cleanupMu.Unlock()

	fmt.Println("\n[CLEANUP] Restoring network configuration...")
	// Идем с конца (LIFO), чтобы сначала удалить маршруты, потом интерфейсы
	for i := len(cleanupCmds) - 1; i >= 0; i-- {
		cmdArgs := cleanupCmds[i]
		fmt.Printf("Running: %s %v\n", cmdArgs[0], cmdArgs[1:])
		exec.Command(cmdArgs[0], cmdArgs[1:]...).Run()
	}
	cleanupCmds = nil // Очищаем список
}

// Структура для возврата данных о шлюзе
type GwInfo struct {
	Ip  string
	Dev string
}

func getDefaultGatewayParams() (GwInfo, error) {
	out, err := exec.Command("ip", "route", "show", "default").Output()
	if err != nil {
		return GwInfo{}, err
	}
	// "default via 192.168.1.1 dev enp1s0 proto dhcp ..."
	fields := strings.Fields(string(out))
	if len(fields) > 4 && fields[0] == "default" && fields[1] == "via" {
		return GwInfo{Ip: fields[2], Dev: fields[4]}, nil
	}
	return GwInfo{}, fmt.Errorf("parse error")
}

// Включает глобальный перехват трафика
func enableGlobalTunRouting() {
	fmt.Println("[VPN] Enabling Global Routing (0.0.0.0/1)...")

	routes := []string{"0.0.0.0/1", "128.0.0.0/1"}
	for _, r := range routes {
		err := exec.Command("ip", "route", "add", r, "dev", "obfsvpn").Run()
		if err == nil {
			registerCleanup([]string{"ip", "route", "del", r})
		}
	}
}
