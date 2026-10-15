package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"surplustoken-connect/internal/configuration"
	"surplustoken-connect/internal/gateway"
	"time"
)

type CredentialStore interface {
	Load() (string, error)
	Save(string) error
	Delete() error
}

func (a *API) remember(id string, local *localSession, response any) {
	if a.Store == nil {
		return
	}
	a.storeMu.Lock()
	a.savedID = id
	a.clearPending = false
	a.storeVersion++
	a.storeMu.Unlock()
	err := local.remote.Remember(func(token string) error {
		a.storeMu.Lock()
		defer a.storeMu.Unlock()
		if a.savedID != id || a.clearPending {
			return nil
		} // Older sessions cannot overwrite a newer login.
		if err := a.Store.Save(token); err != nil {
			return err
		}
		a.storeVersion++
		return nil
	})
	if err != nil {
		response.(map[string]any)["warning"] = err.Error() + "；本次登录仍可使用"
	}
}
func (a *API) forgetSaved(id string) error {
	a.storeMu.Lock()
	defer a.storeMu.Unlock()
	if id == "" || a.Store == nil || a.savedID != id {
		return nil
	}
	return a.deleteSavedLocked()
}

// The pending marker prevents an in-flight refresh from re-saving a credential
// whose removal failed. Logout or resume can retry the same cleanup explicitly.
func (a *API) deleteSavedLocked() error {
	a.clearPending = true
	if err := a.Store.Delete(); err != nil {
		return fmt.Errorf("无法清除本机记住的登录，请重试退出登录或恢复登录：%w", err)
	}
	a.savedID = ""
	a.clearPending = false
	a.storeVersion++
	return nil
}

func (a *API) failSession(w http.ResponseWriter, id string, local *localSession, err error, authLocked bool) {
	var expired *gateway.SessionExpiredError
	if !errors.As(err, &expired) {
		fail(w, err)
		return
	}
	if !authLocked {
		a.authMu.Lock()
		defer a.authMu.Unlock()
	}
	a.mu.Lock()
	if a.sessions[id] == local {
		delete(a.sessions, id)
	}
	a.mu.Unlock()
	a.Engine.Forget(id)
	message := expired.Error()
	cleanupErr := a.forgetSaved(id)
	if cleanupErr != nil {
		message += "；" + cleanupErr.Error()
	}
	respond(w, http.StatusUnauthorized, map[string]any{"error": message, "credential_cleanup_required": cleanupErr != nil})
}

func (a *API) resume(w http.ResponseWriter, r *http.Request) {
	if a.Store == nil {
		respond(w, 200, map[string]bool{"saved": false})
		return
	}
	a.storeMu.Lock()
	if a.clearPending {
		err := a.deleteSavedLocked()
		a.storeMu.Unlock()
		if err != nil {
			fail(w, err)
			return
		}
		respond(w, 200, map[string]any{"saved": false, "warning": "已清除失效的登录，请重新登录"})
		return
	}
	id := a.savedID
	a.storeMu.Unlock()
	if local := a.session(id); local != nil {
		v, err := a.finishLogin(r.Context(), id, local)
		if err != nil {
			a.failSession(w, id, local, err, true)
			return
		}
		respond(w, 200, v)
		return
	}
	a.storeMu.Lock()
	token, err := a.Store.Load()
	version := a.storeVersion
	a.storeMu.Unlock()
	if err != nil {
		fail(w, err)
		return
	}
	if token == "" {
		respond(w, 200, map[string]bool{"saved": false})
		return
	}
	remote, err := a.Gateway.Resume(r.Context(), token)
	token = ""
	if err != nil {
		var expired *gateway.SessionExpiredError
		if errors.As(err, &expired) {
			a.storeMu.Lock()
			if a.storeVersion != version {
				a.storeMu.Unlock()
				fail(w, errors.New("登录状态已更新，请重试恢复登录"))
				return
			}
			deleteErr := a.deleteSavedLocked()
			a.storeMu.Unlock()
			if deleteErr != nil {
				fail(w, deleteErr)
				return
			}
			respond(w, 200, map[string]any{"saved": false, "warning": "登录已过期，请重新登录"})
			return
		}
		fail(w, errors.New("暂时无法恢复登录，请检查网络后重试"))
		return
	}
	id = configuration.ID()
	local := &localSession{remote: remote, created: time.Now(), ready: true}
	a.mu.Lock()
	a.sessions[id] = local
	a.mu.Unlock()
	// Save rotated token even if fetching the profile or keys subsequently fails.
	metadata := map[string]any{}
	a.remember(id, local, metadata)
	v, err := a.finishLogin(r.Context(), id, local)
	if err != nil {
		a.failSession(w, id, local, err, true)
		return
	}
	if warning, ok := metadata["warning"]; ok {
		v.(map[string]any)["warning"] = warning
	}
	respond(w, 200, v)
}
