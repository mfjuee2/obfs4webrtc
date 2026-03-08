# obfs4webrtc

Данный проект является исследовательской работой (Proof of Concept), демонстрирующей возможности инкапсуляции сетевого трафика через нестандартные каналы связи (WebRTC DataChannels). Проект не предназначен для нарушения работы сервисов или обхода биллинга провайдеров.

obfs4webrtc is an educational test harness for studying how application traffic—such as media streams—behaves when encapsulated and routed through system tunnels (for example, WireGuard) in controlled laboratory environments.  
It exposes local proxy endpoints (SOCKS/HTTP), captures detailed metrics and logs (latency, throughput, retransmits), and offers repeatable test profiles so researchers and students can measure protocol interactions and routing behavior without altering system‑wide networking.

**Important:** Obfsru is intended for lawful research, teaching, and lab testing only. It does not provide instructions for evading network controls or routing traffic through third‑party services without permission.

# Failures
Rutube may be very slow or impossible to load due to buffering.

# TODO
https://telemost.yandex.ru/

# Install
- go mod tidy
- go build