// Package mcsrvstat — публичный статус Minecraft-серверов (порт mcsrvstat.py).
//
// Порядок источников: REST mcsrvstat.us -> modern Server List Ping ->
// legacy ping (0xFE 0x01). Никогда не бросает ошибок наружу:
// недоступность источника = offline-статус.
package mcsrvstat

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// Status — стандартизированный статус сервера.
type Status struct {
	IP            string
	IsOnline      bool
	PlayersOnline int
	PlayersMax    int
	PlayerNames   []string
	PlayerList    []string
	Version       string
	Port          int // 0 — неизвестен
}

const (
	apiURL        = "https://api.mcsrvstat.us/3/%s"
	mcstatusURL   = "https://api.mcstatus.io/v2/status/java/%s"
	legacyPingTO  = 5 * time.Second
	modernSLPTO   = 4 * time.Second
	requestTO     = 7 * time.Second
	playersListTO = 8 * time.Second
	httpUA        = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36" +
		" (KHTML, like Gecko) Chrome/126.0 Safari/537.36"
)

func emptyStatus(serverIP string) Status {
	return Status{
		IP:      serverIP,
		Version: "Неизвестно",
	}
}

// GetServerStatus возвращает статус сервера по адресу (ip + опциональный порт).
//
// allowLegacy=false отключает legacy-пинг: прокси Aternos отвечает на нём даже
// на оффлайн-серверах (фейковый «онлайн»), поэтому когда панель недоступна,
// legacy использовать нельзя.
func GetServerStatus(serverIP string, port int, allowLegacy bool) Status {
	if serverIP == "" {
		return emptyStatus("")
	}

	host := serverIP
	explicitPort := port
	if i := strings.LastIndex(serverIP, ":"); i > 0 && !strings.HasPrefix(serverIP, "[") {
		if p, err := strconv.Atoi(serverIP[i+1:]); err == nil {
			host = serverIP[:i]
			explicitPort = p
		}
	}

	api := apiStatus(serverIP)
	if api != nil && api.IsOnline {
		api.PlayerList = getPlayersList(host, api.Port)
		return *api
	}

	apiPort := 0
	if api != nil && api.IsOnline {
		apiPort = api.Port
	}
	targetPort := apiPort
	if targetPort == 0 {
		targetPort = explicitPort
	}
	if targetPort == 0 {
		targetPort = 25565
	}

	if slp := modernSLP(host, targetPort); slp != nil {
		slp.PlayerList = getPlayersList(host, slp.Port)
		return *slp
	}

	if allowLegacy {
		if legacy := legacyPing(host, targetPort); legacy != nil {
			legacy.PlayerList = getPlayersList(host, legacy.Port)
			return *legacy
		}
	}

	if api != nil {
		return *api
	}
	return emptyStatus(serverIP)
}

// ------------------------------------------------------------------ //
// REST API mcsrvstat.us
// ------------------------------------------------------------------ //

type mcsrvAPIResponse struct {
	Online  bool   `json:"online"`
	IP      string `json:"ip"`
	Port    int    `json:"port"`
	Version string `json:"version"`
	Players struct {
		Online int `json:"online"`
		Max    int `json:"max"`
		List   []struct {
			Name string `json:"name"`
		} `json:"list"`
	} `json:"players"`
}

func apiStatus(serverIP string) *Status {
	client := &httpClient{timeout: requestTO}
	body, err := client.get(fmt.Sprintf(apiURL, serverIP))
	if err != nil {
		return nil
	}
	var data mcsrvAPIResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil
	}
	names := make([]string, 0, len(data.Players.List))
	for _, p := range data.Players.List {
		if p.Name != "" {
			names = append(names, p.Name)
		}
	}
	version := data.Version
	if version == "" {
		version = "Неизвестно"
	}
	return &Status{
		IP:            serverIP,
		IsOnline:      data.Online,
		PlayersOnline: data.Players.Online,
		PlayersMax:    data.Players.Max,
		PlayerNames:   names,
		Version:       version,
		Port:          data.Port,
	}
}

// GetPlayersList возвращает ники игроков через mcstatus.io (пусто при ошибке).
func GetPlayersList(serverIP string, port int) []string {
	return getPlayersList(serverIP, port)
}

func getPlayersList(serverIP string, port int) []string {
	addr := serverIP
	if port != 0 && port != 25565 {
		addr = fmt.Sprintf("%s:%d", serverIP, port)
	}
	client := &httpClient{timeout: playersListTO}
	body, err := client.get(fmt.Sprintf(mcstatusURL, addr))
	if err != nil {
		return nil
	}
	var data struct {
		Players struct {
			List []struct {
				NameClean string `json:"name_clean"`
				NameRaw   string `json:"name_raw"`
			} `json:"list"`
		} `json:"players"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil
	}
	var names []string
	for _, p := range data.Players.List {
		name := p.NameClean
		if name == "" {
			name = p.NameRaw
		}
		if name == "" {
			name = "???"
		}
		names = append(names, name)
	}
	return names
}

// ------------------------------------------------------------------ //
// modern Server List Ping (1.7+)
// ------------------------------------------------------------------ //

func modernSLP(host string, port int) *Status {
	if port == 0 {
		port = 25565
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, modernSLPTO)
	if err != nil {
		return nil
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(modernSLPTO))

	// Handshake: proto=4, host, port, next=1 (status).
	handshake := []byte{0x00, 0x04}
	handshake = append(handshake, varint(len(host))...)
	handshake = append(handshake, []byte(host)...)
	handshake = binary.BigEndian.AppendUint16(handshake, uint16(port))
	handshake = append(handshake, 0x01)

	packet := append(varint(len(handshake)), handshake...)
	packet = append(packet, 0x01, 0x00) // Status Request
	if _, err := conn.Write(packet); err != nil {
		return nil
	}

	head := make([]byte, 5)
	if _, err := io.ReadFull(conn, head); err != nil {
		return nil
	}
	if head[0] != 0x01 {
		return nil
	}
	length, err := readVarint(head[1:])
	if err != nil {
		return nil
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return nil
	}

	var data struct {
		Version struct {
			Name string `json:"name"`
		} `json:"version"`
		Players struct {
			Online int `json:"online"`
			Max    int `json:"max"`
			Sample []struct {
				Name string `json:"name"`
			} `json:"sample"`
		} `json:"players"`
	}
	if err := json.Unmarshal(payload, &data); err != nil {
		return nil
	}
	names := make([]string, 0, len(data.Players.Sample))
	for _, p := range data.Players.Sample {
		if p.Name != "" {
			names = append(names, p.Name)
		}
	}
	version := data.Version.Name
	if version == "" {
		version = "Неизвестно"
	}
	return &Status{
		IP:            fmt.Sprintf("%s:%d", host, port),
		Port:          port,
		IsOnline:      true,
		PlayersOnline: data.Players.Online,
		PlayersMax:    data.Players.Max,
		PlayerNames:   names,
		Version:       version,
	}
}

// ------------------------------------------------------------------ //
// legacy Server List Ping (0xFE 0x01)
// ------------------------------------------------------------------ //

func legacyPing(host string, port int) *Status {
	if port == 0 {
		port = 25565
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, legacyPingTO)
	if err != nil {
		return nil
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(legacyPingTO))

	if _, err := conn.Write([]byte{0xfe, 0x01}); err != nil {
		return nil
	}
	head := make([]byte, 3)
	if _, err := io.ReadFull(conn, head); err != nil {
		return nil
	}
	if head[0] != 0xff || head[1] != 0x00 {
		return nil
	}
	remaining := make([]byte, int(head[2])*2)
	if _, err := io.ReadFull(conn, remaining); err != nil {
		return nil
	}

	u16 := make([]uint16, 0, len(remaining)/2)
	for i := 0; i+1 < len(remaining); i += 2 {
		u16 = append(u16, binary.BigEndian.Uint16(remaining[i:i+2]))
	}
	text := string(utf16.Decode(u16))

	var fields []string
	for _, f := range strings.Split(text, "\x00") {
		if f = strings.TrimSpace(f); f != "" {
			fields = append(fields, f)
		}
	}
	if len(fields) < 3 {
		return nil
	}

	toInt := func(s string) int {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return 0
		}
		return n
	}

	var online, max int
	version := "Неизвестно"
	if len(fields) >= 6 && toInt(fields[1]) > 0 && !isAllDigits(fields[2]) {
		online = toInt(fields[4])
		max = toInt(fields[5])
		version = fields[2]
	} else {
		online = toInt(fields[1])
		max = toInt(fields[2])
	}
	return &Status{
		IP:            fmt.Sprintf("%s:%d", host, port),
		Port:          port,
		IsOnline:      true,
		PlayersOnline: online,
		PlayersMax:    max,
		Version:       version,
	}
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ------------------------------------------------------------------ //
// вспомогательное
// ------------------------------------------------------------------ //

// varint кодирует число в протокольный формат Minecraft.
func varint(v int) []byte {
	var out []byte
	for {
		b := byte(v & 0x7F)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		out = append(out, b)
		if v == 0 {
			return out
		}
	}
}

func readVarint(data []byte) (int, error) {
	var result int
	shift := 0
	for i, b := range data {
		result |= int(b&0x7F) << shift
		if b&0x80 == 0 {
			return result, nil
		}
		shift += 7
		if i >= 4 {
			return 0, fmt.Errorf("varint слишком длинный")
		}
	}
	return 0, fmt.Errorf("varint не завершён")
}

// httpClient — тонкая обёртка net/http с таймаутом.
type httpClient struct {
	timeout time.Duration
	client  *http.Client
}

func (c *httpClient) get(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	if c.client == nil {
		c.client = &http.Client{Timeout: c.timeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", httpUA)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}
