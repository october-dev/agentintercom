package bus

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

type DevicePairing struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	UserCode        string `json:"userCode"`
	DeviceCode      string `json:"deviceCode,omitempty"`
	ExpiresAt       string `json:"expiresAt"`
	VerificationURL string `json:"verificationUrl,omitempty"`
}

type PairedDevice struct {
	Status      string           `json:"status"`
	WorkspaceID string           `json:"workspaceId,omitempty"`
	Device      *WorkspaceDevice `json:"device,omitempty"`
}

func (a *HostedAccounts) BeginDevicePairing(ctx context.Context, name string) (DevicePairing, error) {
	var result DevicePairing
	if err := validateText(name, "name", 100, false); err != nil {
		return result, err
	}
	id, err := randomID("pair_")
	if err != nil {
		return result, err
	}
	secret, err := randomValue(32)
	if err != nil {
		return result, err
	}
	var raw [5]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return result, err
	}
	code := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:])
	code = code[:4] + "-" + code[4:]
	tx, err := a.store.beginTx(ctx, "device-pairing/registration")
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	now := nowMillis()
	if _, err := tx.ExecContext(ctx, "DELETE FROM hosted_pairings WHERE expires_at<=?", now); err != nil {
		return result, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM hosted_pairings").Scan(&count); err != nil {
		return result, err
	}
	if count >= 5000 {
		return result, Errorf(CodeBackpressure, "Device connection requests are busy; retry shortly")
	}
	expires := now + (10 * time.Minute).Milliseconds()
	if _, err := tx.ExecContext(ctx, "INSERT INTO hosted_pairings(pairing_id,user_code,device_code_hash,name,expires_at) VALUES(?,?,?,?,?)", id, code, tokenDigest(secret), name, expires); err != nil {
		return result, err
	}
	result = DevicePairing{ID: id, Name: name, UserCode: code, DeviceCode: secret, ExpiresAt: instant(expires)}
	return result, tx.Commit()
}

func normalizedPairingCode(code string) string { return strings.ToUpper(strings.TrimSpace(code)) }

func (a *HostedAccounts) DevicePairing(ctx context.Context, code string) (DevicePairing, error) {
	var result DevicePairing
	code = normalizedPairingCode(code)
	if len(code) != 9 {
		return result, Errorf(CodeNotFound, "Device request expired or was already completed")
	}
	var expires int64
	err := a.store.db.QueryRowContext(ctx, "SELECT pairing_id,name,user_code,expires_at FROM hosted_pairings WHERE user_code=? AND status='pending' AND expires_at>?", code, nowMillis()).Scan(&result.ID, &result.Name, &result.UserCode, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return result, Errorf(CodeNotFound, "Device request expired or was already completed")
	}
	result.ExpiresAt = instant(expires)
	return result, err
}

func (a *HostedAccounts) ApproveDevicePairing(ctx context.Context, user HostedUser, code, scopeID string, approve bool) error {
	return a.completeDevicePairing(ctx, user, code, scopeID, "", approve)
}

func (a *HostedAccounts) completeDevicePairing(ctx context.Context, user HostedUser, code, scopeID, replaceDeviceID string, approve bool) error {
	if err := validateIdentity(replaceDeviceID, "replaceDeviceId", true); err != nil {
		return err
	}
	code = normalizedPairingCode(code)
	tx, err := a.store.beginTx(ctx, scopeID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	status := "denied"
	if approve {
		status = "approved"
		if _, err := hostedMember(ctx, tx, scopeID, user.ID); err != nil {
			return err
		}
	}
	// The conditional update also locks this request across simultaneous choices
	// of different workspaces. Failure later rolls the approval back atomically.
	result, err := tx.ExecContext(ctx, "UPDATE hosted_pairings SET status=? WHERE user_code=? AND status='pending' AND expires_at>?", status, code, nowMillis())
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return Errorf(CodeConflict, "Device request expired or was already completed")
	}
	if approve {
		var id, name string
		if err := tx.QueryRowContext(ctx, "SELECT pairing_id,name FROM hosted_pairings WHERE user_code=?", code).Scan(&id, &name); err != nil {
			return err
		}
		var device WorkspaceDevice
		if replaceDeviceID == "" {
			device, err = a.enrollDeviceTx(ctx, tx, user, scopeID, name, "device")
		} else {
			device, err = a.replaceConnectionTx(ctx, tx, user, scopeID, replaceDeviceID, name, "device")
		}
		if err != nil {
			return err
		}
		data, err := json.Marshal(device)
		if err != nil {
			return err
		}
		encrypted, err := a.seal(string(data), "pairing/"+id)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE hosted_pairings SET scope_id=?,device_id=?,credential=? WHERE pairing_id=?", scopeID, device.ID, encrypted, id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if approve {
		a.runtime.signals.notifyAllScope(scopeID)
	}
	return nil
}

func (a *HostedAccounts) PollDevicePairing(ctx context.Context, secret string) (PairedDevice, error) {
	var result PairedDevice
	if len(secret) != 43 {
		return result, Errorf(CodeUnauthenticated, "Device request is invalid or expired")
	}
	tx, err := a.store.beginTx(ctx, "device-pairing/"+tokenDigest(secret))
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var id string
	var scope, payload sql.NullString
	var last int64
	err = tx.QueryRowContext(ctx, "SELECT pairing_id,status,scope_id,credential,last_poll_at FROM hosted_pairings WHERE device_code_hash=? AND expires_at>?", tokenDigest(secret), nowMillis()).Scan(&id, &result.Status, &scope, &payload, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return result, Errorf(CodeUnauthenticated, "Device request is invalid or expired")
	}
	if err != nil {
		return result, err
	}
	if last > nowMillis()-4000 {
		return result, Errorf(CodeBackpressure, "Poll at most once every five seconds")
	}
	if _, err := tx.ExecContext(ctx, "UPDATE hosted_pairings SET last_poll_at=? WHERE pairing_id=?", nowMillis(), id); err != nil {
		return result, err
	}
	if result.Status == "approved" {
		plain, err := a.unseal(payload.String, "pairing/"+id)
		if err != nil {
			return result, err
		}
		var device WorkspaceDevice
		if err := json.Unmarshal([]byte(plain), &device); err != nil {
			return result, err
		}
		if _, err := authenticateHostedDevice(ctx, tx, device.Token); err != nil {
			return result, err
		}
		result.WorkspaceID, result.Device = scope.String, &device
	}
	// Repeated successful polling within the short approval window recovers a
	// lost response. Only the original random device secret can obtain the token.
	return result, tx.Commit()
}

func (h *HostedServer) registerDevicePairingRoutes() {
	creation := rate.NewLimiter(1, 20)
	polling := rate.NewLimiter(50, 100)
	h.routes.HandleFunc("POST /api/device/authorizations", func(w http.ResponseWriter, r *http.Request) {
		if !creation.Allow() {
			writeFailure(w, Errorf(CodeBackpressure, "Device requests are busy; retry shortly"))
			return
		}
		var input struct {
			Name string `json:"name"`
		}
		if err := decodeBoundedBody(w, r, &input, 2048, "2 KiB"); err != nil {
			writeFailure(w, err)
			return
		}
		result, err := h.accounts.BeginDevicePairing(r.Context(), input.Name)
		if err != nil {
			writeFailure(w, err)
			return
		}
		result.VerificationURL = h.publicURL + "/app#device=" + result.UserCode
		writeResult(w, 201, result)
	})
	h.routes.HandleFunc("POST /api/device/authorizations/token", func(w http.ResponseWriter, r *http.Request) {
		if !polling.Allow() {
			writeFailure(w, Errorf(CodeBackpressure, "Device requests are busy; retry shortly"))
			return
		}
		var input struct {
			DeviceCode string `json:"deviceCode"`
		}
		if err := decodeBoundedBody(w, r, &input, 2048, "2 KiB"); err != nil {
			writeFailure(w, err)
			return
		}
		result, err := h.accounts.PollDevicePairing(r.Context(), input.DeviceCode)
		if err != nil {
			writeFailure(w, err)
			return
		}
		writeResult(w, 200, result)
	})
	h.routes.HandleFunc("GET /api/device/authorizations/{code}", h.userRoute(func(ctx context.Context, _ HostedUser, _ http.ResponseWriter, r *http.Request) (any, error) {
		return h.accounts.DevicePairing(ctx, r.PathValue("code"))
	}))
	h.routes.HandleFunc("POST /api/device/authorizations/{code}", h.userRoute(func(ctx context.Context, u HostedUser, w http.ResponseWriter, r *http.Request) (any, error) {
		var input struct {
			WorkspaceID     string `json:"workspaceId"`
			ReplaceDeviceID string `json:"replaceDeviceId"`
			Approve         bool   `json:"approve"`
		}
		if err := decodeBody(w, r, &input); err != nil {
			return nil, err
		}
		return map[string]bool{"completed": true}, h.accounts.completeDevicePairing(ctx, u, r.PathValue("code"), input.WorkspaceID, input.ReplaceDeviceID, input.Approve)
	}))
	h.routes.HandleFunc("DELETE /api/device/self", func(w http.ResponseWriter, r *http.Request) {
		token, err := bearer(r)
		if err != nil {
			writeFailure(w, err)
			return
		}
		d, err := authenticateHostedDevice(r.Context(), h.accounts.store.db, token)
		if err != nil {
			writeFailure(w, err)
			return
		}
		if err := h.accounts.RevokeDevice(r.Context(), HostedUser{ID: d.UserID}, d.ScopeID, d.ID); err != nil {
			writeFailure(w, err)
			return
		}
		writeResult(w, 200, map[string]bool{"revoked": true})
	})
}

func (c Client) BeginDevicePairing(ctx context.Context, name string) (DevicePairing, error) {
	return request[DevicePairing](ctx, c, http.MethodPost, "/api/device/authorizations", map[string]string{"name": name})
}
func (c Client) PollDevicePairing(ctx context.Context, secret string) (PairedDevice, error) {
	return request[PairedDevice](ctx, c, http.MethodPost, "/api/device/authorizations/token", map[string]string{"deviceCode": secret})
}
func (c Client) StartDeviceSession(ctx context.Context, name, resumeID string) (RegisterAgentResult, error) {
	return request[RegisterAgentResult](ctx, c, http.MethodPost, "/api/device/sessions", map[string]string{"name": name, "resumeId": resumeID})
}
func (c Client) RevokeDevice(ctx context.Context) error {
	_, err := request[map[string]bool](ctx, c, http.MethodDelete, "/api/device/self", nil)
	return err
}
