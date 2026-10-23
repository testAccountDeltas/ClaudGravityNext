// Part of the dashboard package's port from superrelay (v1)'s ui.go —
// see register.go for the package doc comment. This file holds the
// "add Google account" OAuth login/callback flow.
package dashboard

import (
	"fmt"
	"net/http"

	"claudgravity/internal/accounts"
)

func handleOAuthLogin(w http.ResponseWriter, r *http.Request) {
	state := accounts.NewOAuthState()
	authURL := accounts.BuildGoogleAuthURL(state)
	http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
}

func handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if !accounts.ConsumeOAuthState(state) {
		// Classic OAuth CSRF: without this check, a forged callback URL
		// (any attacker-controlled "code") would be exchanged and bound
		// into this gateway's account pool as if the admin initiated it.
		http.Error(w, "Invalid or expired OAuth state", http.StatusBadRequest)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "Missing authorization code", http.StatusBadRequest)
		return
	}

	acc, err := accounts.ExchangeAuthCode(code)
	if err != nil {
		http.Error(w, fmt.Sprintf("OAuth exchange error: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><title>Авторизация успешна</title><meta charset="utf-8"></head>
<body style="font-family:sans-serif;background:#090d16;color:#f8fafc;padding:40px;text-align:center;">
  <h2 style="color:#10b981;">✓ Аккаунт успешно добавлен!</h2>
  <p>Email: <b>%s</b></p>
  <p><a href="/" style="display:inline-block;padding:8px 16px;background:#3b82f6;color:white;text-decoration:none;border-radius:6px;font-size:13px;font-weight:600;">Вернуться в консоль</a></p>
</body>
</html>`, acc.Email)
	_, _ = w.Write([]byte(html))
}
