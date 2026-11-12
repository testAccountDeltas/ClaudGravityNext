// Part of the dashboard package — see oauth_api.go for the login/callback
// flow this complements.
package dashboard

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"

	"claudgravity/internal/accounts"
)

// Google only accepts the redirect URI registered for the Antigravity OAuth
// client (http://localhost:8898/oauth/callback); a server's public address is
// rejected with invalid_request. On a remote gateway the browser therefore
// lands on a dead localhost page after sign-in, but its address bar already
// holds code+state. This page lets the admin paste that address so the
// gateway can finish the exchange itself.

const oauthPastePage = `<!DOCTYPE html>
<html>
<head><title>Добавить аккаунт</title><meta charset="utf-8"></head>
<body style="font-family:sans-serif;background:#090d16;color:#f8fafc;padding:40px;max-width:720px;margin:auto;">
  <h2>Завершение входа в Google</h2>
  <ol style="line-height:1.7;color:#cbd5e1;">
    <li><a href="/oauth/login" target="_blank" style="color:#60a5fa;">Войти через Google</a> (откроется новая вкладка).</li>
    <li>После входа браузер перейдёт на <code>localhost:8898/oauth/callback?...</code> и покажет ошибку — это нормально.</li>
    <li>Скопируйте <b>весь адрес</b> из адресной строки и вставьте сюда.</li>
  </ol>
  <form method="POST" action="/oauth/paste">
    <textarea name="url" rows="4" style="width:100%%;background:#0f172a;color:#f8fafc;border:1px solid #334155;border-radius:6px;padding:8px;" placeholder="http://localhost:8898/oauth/callback?state=...&code=..."></textarea>
    <p><button type="submit" style="padding:8px 16px;background:#3b82f6;color:white;border:0;border-radius:6px;font-weight:600;cursor:pointer;">Добавить аккаунт</button></p>
  </form>
  %s
</body>
</html>`

func handleOAuthPaste(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method != http.MethodPost {
		_, _ = fmt.Fprintf(w, oauthPastePage, "")
		return
	}
	if err := r.ParseForm(); err != nil {
		writePasteError(w, http.StatusBadRequest, "Не удалось прочитать форму.")
		return
	}
	state, code, ok := parseOAuthRedirect(r.PostFormValue("url"))
	if !ok {
		writePasteError(w, http.StatusBadRequest, "В адресе нет параметров code и state. Скопируйте адрес целиком.")
		return
	}
	if !accounts.ConsumeOAuthState(state) {
		writePasteError(w, http.StatusBadRequest, "Неверный или просроченный state. Начните вход заново.")
		return
	}
	acc, err := accounts.ExchangeAuthCode(code)
	if err != nil {
		writePasteError(w, http.StatusInternalServerError, "Ошибка обмена кода: "+err.Error())
		return
	}
	_, _ = w.Write([]byte(oauthSuccessPage(acc.Email)))
}

// parseOAuthRedirect pulls state and code out of a pasted redirect address
// (or a bare query string).
func parseOAuthRedirect(raw string) (state, code string, ok bool) {
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, "?"); i >= 0 {
		raw = raw[i+1:]
	}
	q, err := url.ParseQuery(raw)
	if err != nil {
		return "", "", false
	}
	state, code = q.Get("state"), q.Get("code")
	return state, code, state != "" && code != ""
}

func writePasteError(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, oauthPastePage, `<p style="color:#f87171;">`+html.EscapeString(msg)+`</p>`)
}
