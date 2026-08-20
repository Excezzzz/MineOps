// Package aternos — минималистичный HTTP-клиент Aternos (Go-порт python-aternos 3.0.6).
//
// Реверс-инжиниринг протокола:
//   - AJAX_TOKEN вычисляется JavaScript-кодом со страницы https://aternos.org/go/
//     (функция `(() => {...})();` выполняется встроенным JS-движком goja с теми же
//     стабами окружения, что js2py в Python-версии);
//   - SEC генерируется клиентом: `ключ:значение` (11 символов [a-z0-9] + "00000")
//     плюс кука ATERNOS_SEC_<ключ>=<значение>;
//   - запросы к /ajax/server/<action> отправляются с куками ATERNOS_SESSION,
//     ATERNOS_SEC_<ключ>, ATERNOS_SERVER=<servid> и параметрами TOKEN/SEC;
//   - статус сервера берётся из JSON `var lastStatus = {...};` на странице /server;
//   - список серверов — с /servers/ (div.server-body, атрибут data-id); редирект
//     на /go/ означает, что кука истекла.
package aternos

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"mineops/internal/util"

	"github.com/dop251/goja"
)

// ------------------------------------------------------------------ //
// ошибки
// ------------------------------------------------------------------ //

const (
	msgCloudflare = "⚠️ Aternos временно заблокировал запрос (Cloudflare). " +
		"Подождите 3-5 минут или обновите куку /set_session."
	msgSessionExpired = "⚠️ Сессия Aternos истекла или недействительна!\n" +
		"Обновите куку командой /set_session в личке с ботом."
	msgOwnerNotFound  = "Владелец не найден: пройдите онбординг заново."
	msgSessionNotSet  = "Сессия Aternos не настроена: выполните /set_session."
	msgServerNotFound = "Сервер не найден или не принадлежит владельцу."
)

// Error — ошибка взаимодействия с Aternos.
type Error struct {
	Message string
}

func (e *Error) Error() string { return e.Message }

// NewError создаёт AternosError с заданным сообщением.
func NewError(message string) *Error { return &Error{Message: message} }

func isCloudflare(err error) bool {
	var aerr *Error
	return errors.As(err, &aerr) && strings.Contains(aerr.Message, "Cloudflare")
}

// ServerBrief — краткая информация о сервере аккаунта.
type ServerBrief struct {
	AternosID   string
	ServerIP    string
	DisplayName string
}

// ServerInfo — данные `lastStatus` с панели Aternos.
type ServerInfo struct {
	Status     int
	Players    any
	Slots      any
	Queue      any
	Lang       string
	Port       any
	Version    string
	PlayerList []string
	Name       string
	IP         string
}

// ------------------------------------------------------------------ //
// утилиты
// ------------------------------------------------------------------ //

// ------------------------------------------------------------------ //
// токен: парсинг и выполнение JS
// ------------------------------------------------------------------ //

var arrowFnRe = regexp.MustCompile(`\(\(\).*?\)\(\);`)

// extractTokenJS извлекает JavaScript-функцию из <head> страницы /go/.
func extractTokenJS(page string) (string, error) {
	head := page
	if i := strings.Index(page, "<head>"); i >= 0 {
		head = page[i+len("<head>"):]
		if j := strings.Index(head, "</head>"); j >= 0 {
			head = head[:j]
		}
	}
	codes := arrowFnRe.FindAllString(head, -1)
	if len(codes) == 0 {
		return "", fmt.Errorf("не удалось найти JS-функцию токена")
	}
	code := codes[0]
	if len(codes) > 1 {
		code = codes[1]
	}
	return code, nil
}

// execTokenJS выполняет JS-функцию токена и возвращает AJAX_TOKEN.
// Окружение повторяет стабы js2py из Python-версии, поэтому значение
// совпадает с тем, что получает python-aternos.
func execTokenJS(code string) (string, error) {
	vm := goja.New()

	stubFn := func(call goja.FunctionCall) goja.Value { return goja.Undefined() }

	window := vm.NewObject()
	if err := vm.Set("window", window); err != nil {
		return "", err
	}
	windowObj := window.ToObject(vm)
	_ = windowObj.Set("Map", stubFn)
	_ = windowObj.Set("setTimeout", stubFn)
	_ = windowObj.Set("setInterval", stubFn)
	_ = windowObj.Set("encodeURIComponent", stubFn)

	doc := vm.NewObject()
	_ = windowObj.Set("document", doc)
	docObj := doc.ToObject(vm)
	_ = docObj.Set("doctype", vm.NewObject())
	_ = docObj.Set("currentScript", vm.NewObject())
	_ = docObj.Set("getElementById", stubFn)
	_ = docObj.Set("prepend", stubFn)
	_ = docObj.Set("append", stubFn)
	_ = docObj.Set("appendChild", stubFn)

	if err := vm.Set("atob", func(s string) string {
		raw, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return ""
		}
		return string(raw)
	}); err != nil {
		return "", err
	}

	if _, err := vm.RunString(code); err != nil {
		return "", fmt.Errorf("JS-выполнение токена не удалось: %w", err)
	}
	tokenVal := windowObj.Get("AJAX_TOKEN")
	if tokenVal == nil || goja.IsUndefined(tokenVal) || goja.IsNull(tokenVal) {
		return "", fmt.Errorf("AJAX_TOKEN не вычислен")
	}
	return tokenVal.String(), nil
}

// fetchToken получает AJAX_TOKEN со страницы /go/.
func fetchToken(ctx context.Context, httpClient *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/go/", nil)
	if err != nil {
		return "", err
	}
	setBrowserHeaders(req, nil, false)
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return "", errCloudflare
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("aternos /go/ вернул HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	code, err := extractTokenJS(string(body))
	if err != nil {
		return "", err
	}
	return execTokenJS(code)
}

// errCloudflare сигнализирует о Cloudflare-блокировке (403).
var errCloudflare = errors.New("cloudflare challenge")

// ------------------------------------------------------------------ //
// SEC
// ------------------------------------------------------------------ //

const secAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

func genSecPart() string {
	buf := make([]byte, 11)
	if _, err := rand.Read(buf); err != nil {
		// Псевдослучайный фолбэк (не должен случиться).
		for i := range buf {
			buf[i] = byte(i*7 + 13)
		}
	}
	var sb strings.Builder
	for _, b := range buf {
		sb.WriteByte(secAlphabet[int(b)%len(secAlphabet)])
	}
	sb.WriteString("00000")
	return sb.String()
}

// ------------------------------------------------------------------ //
// Session — транспорт для одного аккаунта
// ------------------------------------------------------------------ //

const (
	baseURL   = "https://aternos.org"
	requestUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/99.0.4844.84 Safari/537.36 OPR/85.0.4341.47"
	maxRetries   = 5
	requestTO    = 15 * time.Second
	tokenTTL     = 30 * time.Minute
	loginRetryTO = 300 * time.Millisecond
)

// Session — состояние авторизации одного владельца.
type Session struct {
	httpClient *http.Client
	SessionID  string    // ATERNOS_SESSION
	Token      string    // AJAX_TOKEN
	secKey     string    // ATERNOS_SEC_<key> (имя куки)
	secVal     string    // значение куки ATERNOS_SEC_<key>
	expiresAt  time.Time // истечение кеша клиента
}

var lastStatusRe = regexp.MustCompile(`(?s)<script>\s*var lastStatus\s*?=\s*?(\{.+?\});?\s*</script>`)

func setBrowserHeaders(req *http.Request, cookies map[string]string, sendToken bool) {
	req.Header.Set("User-Agent", requestUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	// ВАЖНО: Accept-Encoding НЕ ставим вручную — иначе Go не декомпрессит
	// gzip автоматически и regex по токену не найдёт совпадение.
	req.Header.Set("Referer", baseURL+"/servers/")
	if sendToken {
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	if req.Method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
}

func (s *Session) request(ctx context.Context, method, path string,
	params url.Values, cookies map[string]string, sendToken bool) (*http.Response, error) {
	if sendToken {
		if params == nil {
			params = url.Values{}
		}
		params.Set("TOKEN", s.Token)
		params.Set("SEC", s.secKey+":"+s.secVal)
	}
	u := baseURL + path
	if len(params) > 0 && method != http.MethodPost {
		u += "?" + params.Encode()
	}

	allCookies := map[string]string{
		"ATERNOS_SESSION":         s.SessionID,
		"ATERNOS_SEC_" + s.secKey: s.secVal,
	}
	for k, v := range cookies {
		allCookies[k] = v
	}

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		var body io.Reader
		if method == http.MethodPost && len(params) > 0 {
			body = strings.NewReader(params.Encode())
		}
		req, err := http.NewRequestWithContext(ctx, method, u, body)
		if err != nil {
			return nil, err
		}
		setBrowserHeaders(req, allCookies, sendToken)

		resp, err := s.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("aternos: %w", err)
			time.Sleep(loginRetryTO)
			continue
		}

		ct := resp.Header.Get("Content-Type")
		if resp.StatusCode == http.StatusForbidden && strings.Contains(ct, "text/html") {
			resp.Body.Close()
			lastErr = errCloudflare
			time.Sleep(loginRetryTO)
			continue
		}
		if resp.StatusCode == http.StatusPaymentRequired {
			resp.Body.Close()
			return nil, fmt.Errorf("Aternos: недостаточно прав (402)")
		}
		if resp.StatusCode == http.StatusUnauthorized {
			resp.Body.Close()
			return nil, NewError(msgSessionExpired)
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc := resp.Header.Get("Location")
			resp.Body.Close()
			if strings.HasPrefix(loc, "/go/") {
				return nil, NewError(msgSessionExpired)
			}
			return nil, fmt.Errorf("aternos: неожиданный редирект на %s", loc)
		}
		if resp.StatusCode >= 400 {
			resp.Body.Close()
			return nil, fmt.Errorf("aternos: HTTP %d для %s", resp.StatusCode, path)
		}
		return resp, nil
	}
	if lastErr == errCloudflare {
		return nil, NewError(msgCloudflare)
	}
	return nil, lastErr
}

// newSession логинится по куке: получает токен и генерирует SEC.
func newSession(ctx context.Context, httpClient *http.Client, sessionCookie string) (*Session, error) {
	token, err := fetchToken(ctx, httpClient)
	if err != nil {
		if errors.Is(err, errCloudflare) {
			return nil, NewError(msgCloudflare)
		}
		return nil, NewError(msgSessionExpired)
	}
	key := genSecPart()
	val := genSecPart()
	return &Session{
		httpClient: httpClient,
		SessionID:  sessionCookie,
		Token:      token,
		secKey:     key,
		secVal:     val,
		expiresAt:  time.Now().Add(tokenTTL),
	}, nil
}

// FetchServerInfo получает `lastStatus` сервера.
func (s *Session) FetchServerInfo(ctx context.Context, servID string) (*ServerInfo, error) {
	resp, err := s.request(ctx, http.MethodGet, "/server", nil,
		map[string]string{"ATERNOS_SERVER": servID}, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	match := lastStatusRe.FindSubmatch(body)
	if match == nil {
		return nil, fmt.Errorf("не удалось распарсить lastStatus сервера %s", servID)
	}
	var raw map[string]any
	if err := json.Unmarshal(match[1], &raw); err != nil {
		return nil, fmt.Errorf("битый JSON lastStatus: %w", err)
	}
	info := &ServerInfo{
		Status:     util.ToInt(raw["status"]),
		Players:    raw["players"],
		Slots:      raw["slots"],
		Queue:      raw["queue"],
		Lang:       util.ToStr(raw["lang"]),
		Port:       raw["port"],
		Version:    util.ToStr(raw["version"]),
		PlayerList: util.ToStrList(raw["playerlist"]),
		Name:       util.ToStr(raw["name"]),
		IP:         util.ToStr(raw["ip"]),
	}
	return info, nil
}

// ListServers возвращает серверы аккаунта ({aternos_id, server_ip, display_name}).
// У каждого сервера уточняется ip/имя через FetchServerInfo (best-effort).
func (s *Session) ListServers(ctx context.Context) ([]ServerBrief, error) {
	resp, err := s.request(ctx, http.MethodGet, "/servers/", nil, nil, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	ids := parseServerIDs(string(body))
	var out []ServerBrief
	for _, id := range ids {
		brief := ServerBrief{AternosID: id, DisplayName: id}
		if info, err := s.FetchServerInfo(ctx, id); err == nil {
			brief.ServerIP = info.IP
			brief.DisplayName = util.FirstNonEmpty(info.Name, info.IP, id)
			if brief.ServerIP == "" {
				// IP не отдан панелью — собираем адрес из id сервера
				// (как _get_server_address в python-aternos).
				brief.ServerIP = id + ".aternos.me"
			}
		}
		out = append(out, brief)
	}
	return out, nil
}

var serverBodyDivRe = regexp.MustCompile(`<div\b[^>]*>`)

func parseServerIDs(html string) []string {
	var ids []string
	for _, tag := range serverBodyDivRe.FindAllString(html, -1) {
		if !strings.Contains(tag, `class="server-body"`) {
			continue
		}
		if m := regexp.MustCompile(`data-id="([^"]+)"`).FindStringSubmatch(tag); m != nil {
			if id := strings.TrimSpace(m[1]); id != "" {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// actionServer выполняет ajax-действие над сервером (start/stop/confirm/...).
// Метод POST с данными в теле — как в python-aternos (_urlopen с data).
func (s *Session) actionServer(ctx context.Context, action string, servID string, extra url.Values) (map[string]any, error) {
	resp, err := s.request(ctx, http.MethodPost, "/ajax/server/"+action, extra,
		map[string]string{"ATERNOS_SERVER": servID}, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("aternos: неожиданный ответ %s: %s", action, truncate(string(body), 120))
	}
	return result, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Start запускает сервер; при не принятой EULA сначала принимает её.
func (s *Session) Start(ctx context.Context, servID string) error {
	start := func() (map[string]any, error) {
		return s.actionServer(ctx, "start", servID, url.Values{
			"headstart":      {"0"},
			"access-credits": {"0"},
		})
	}
	result, err := start()
	if err != nil {
		return err
	}
	if success, _ := result["success"].(bool); success {
		return nil
	}
	reason := util.ToStr(result["error"])
	if reason == "eula" {
		if _, err := s.actionServer(ctx, "accept-eula", servID, nil); err != nil {
			log.Printf("aternos: accept-eula не выполнен: %v", err)
		}
		if result, err = start(); err != nil {
			return err
		}
		if success, _ := result["success"].(bool); success {
			return nil
		}
		reason = util.ToStr(result["error"])
	}
	return fmt.Errorf("%s", serverStartReason(reason))
}

func serverStartReason(reason string) string {
	switch reason {
	case "eula":
		return "EULA не принята. Запустите сервер с accepteula=True"
	case "already":
		return "Server has already started"
	case "wrongversion":
		return "Incorrect software version installed"
	case "file":
		return "File server is unavailbale, view https://status.aternos.gmbh"
	case "size":
		return "Available storage size limit (4 GB) has been reached"
	}
	if reason == "" {
		return "Unable to start server"
	}
	return fmt.Sprintf("Unable to start server, code: %s", reason)
}

// Stop останавливает сервер.
func (s *Session) Stop(ctx context.Context, servID string) error {
	_, err := s.actionServer(ctx, "stop", servID, nil)
	return err
}

// Confirm подтверждает запуск сервера из очереди.
func (s *Session) Confirm(ctx context.Context, servID string) error {
	_, err := s.actionServer(ctx, "confirm", servID, nil)
	return err
}
