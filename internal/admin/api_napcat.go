package admin

import (
	"net/http"

	"qqbot/internal/napcat"
)

// handleNapCatStatus NapCat 登录状态。
// GET /api/v1/napcat/status
func (s *Server) handleNapCatStatus(w http.ResponseWriter, r *http.Request) {
	client, err := s.napcatClient()
	if err != nil {
		errorResponse(w, http.StatusServiceUnavailable, CodeNapCatUnavailable, cleanErr(err), nil)
		return
	}
	st, err := client.CheckLogin(r.Context())
	if err != nil {
		errorResponse(w, http.StatusBadGateway, CodeNapCatUnavailable, cleanErr(err), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"is_login":    st.IsLogin,
		"is_offline":  st.IsOffline,
		"login_error": st.LoginError,
		"has_qrcode":  st.QRCodeURL != "",
	})
}

// handleNapCatQRCode 二维码 PNG（后端生成，NapCat token 不进入浏览器）。
// GET /api/v1/napcat/qrcode.png
func (s *Server) handleNapCatQRCode(w http.ResponseWriter, r *http.Request) {
	client, err := s.napcatClient()
	if err != nil {
		errorResponse(w, http.StatusServiceUnavailable, CodeNapCatUnavailable, cleanErr(err), nil)
		return
	}
	content := r.URL.Query().Get("content")
	if content == "" {
		// 未传内容时用 NapCat 当前二维码
		content, err = client.GetQRCode(r.Context())
		if err != nil {
			errorResponse(w, http.StatusBadGateway, CodeNapCatUnavailable, cleanErr(err), nil)
			return
		}
	}
	img, mime, err := napcat.QRImage(r.Context(), content)
	if err != nil {
		errorResponse(w, http.StatusBadGateway, CodeNapCatUnavailable, cleanErr(err), nil)
		return
	}
	// 二维码响应禁止缓存
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(img)
}

// handleNapCatRefresh 刷新二维码。
// POST /api/v1/napcat/qrcode/refresh
func (s *Server) handleNapCatRefresh(w http.ResponseWriter, r *http.Request) {
	client, err := s.napcatClient()
	if err != nil {
		errorResponse(w, http.StatusServiceUnavailable, CodeNapCatUnavailable, cleanErr(err), nil)
		return
	}
	if err := client.RefreshQRCode(r.Context()); err != nil {
		errorResponse(w, http.StatusBadGateway, CodeNapCatUnavailable, cleanErr(err), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
