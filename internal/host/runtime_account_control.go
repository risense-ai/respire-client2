//go:build linux || windows || (darwin && cgo)

package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const controlFrameLimit = 64 * 1024

type runtimeControlRequest struct {
	V              int      `json:"v"`
	ID             string   `json:"id"`
	Method         string   `json:"method"`
	Args           []string `json:"args"`
	OperationID    string   `json:"operation_id,omitempty"`
	IdempotencyKey string   `json:"idempotency_key,omitempty"`
}

func controlUUID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]), nil
}

type runtimeControlBinding struct {
	InstallationRoot  string  `json:"installation_root"`
	Profile           string  `json:"profile"`
	LaunchID          string  `json:"launch_id"`
	ExpectedAccountID *string `json:"expected_account_id"`
}

type runtimeAccountControl struct {
	binding runtimeControlBinding
	image   string
	port    uint64
	grant   map[string]any
}

type runtimeMaintenanceMarker struct {
	Schema          int    `json:"schema"`
	ID              string `json:"id"`
	Token           string `json:"token"`
	Phase           string `json:"phase"`
	ExpectedProfile string `json:"expected_profile"`
	ExpectedVersion string `json:"expected_version"`
	OwnerPID        int    `json:"owner_pid"`
	OwnerBirth      string `json:"owner_birth"`
}

func (m *Manager) runtimeAccountEndpoint() (string, uint64, error) {
	address, err := url.Parse(m.Endpoint)
	if err != nil || address.Scheme != "http" || address.Hostname() != "127.0.0.1" ||
		address.User != nil || address.Path != "" || address.RawQuery != "" || address.Fragment != "" {
		return "", 0, errors.New("Runtime control requires the local loopback endpoint")
	}
	port, err := strconv.ParseUint(address.Port(), 10, 16)
	if err != nil || port == 0 {
		return "", 0, errors.New("Invalid Runtime control port")
	}
	return address.Host, port, nil
}

func (m *Manager) prepareRuntimeAccountControl(ctx context.Context, imagePath, imageDigest, profilePath string,
	expectedAccount *string, ownedMaintenance []byte) (runtimeAccountControl, error) {
	return m.prepareRuntimeControlGrant(ctx, imagePath, imageDigest, profilePath, expectedAccount, ownedMaintenance, "Paused")
}

func (m *Manager) prepareRuntimeControlGrant(ctx context.Context, imagePath, imageDigest, profilePath string,
	expectedAccount *string, ownedMaintenance []byte, phase string) (runtimeAccountControl, error) {
	var control runtimeAccountControl
	root, err := canonicalSessionDirectory(m.DataRoot)
	if err != nil {
		return control, err
	}
	profile, err := canonicalSessionDirectory(profilePath)
	if err != nil {
		return control, err
	}
	image, err := canonicalSessionDirectory(imagePath)
	if err != nil {
		return control, err
	}
	actualImage, err := fileHash(image)
	if err != nil {
		return control, err
	}
	if actualImage != imageDigest {
		return control, errors.New("Runtime control image changed")
	}
	_, port, err := m.runtimeAccountEndpoint()
	if err != nil {
		return control, err
	}
	birth, err := hostControlBirth(ctx)
	if err != nil {
		return control, err
	}
	grantID, err := controlUUID()
	if err != nil {
		return control, err
	}
	grantAccount := controlDigest([]byte(profile))
	if expectedAccount != nil {
		if !validHash(*expectedAccount) {
			return control, errors.New("Invalid Runtime control account binding")
		}
		grantAccount = *expectedAccount
	}
	var registrationDigest any
	registration, err := readCredentialConfig(filepath.Join(root, "runtime", "autostart", "registration.json"), controlFrameLimit)
	if err == nil {
		registrationDigest = controlDigest(registration)
	} else if !errors.Is(err, os.ErrNotExist) {
		return control, err
	}
	maintenancePath := filepath.Join(root, "runtime", "autostart", "maintenance.json")
	markerBytes, err := readCredentialConfig(maintenancePath, runtimeMaintenanceLimit)
	var maintenance any
	if ownedMaintenance == nil {
		if !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return control, err
			}
			return control, errors.New("Runtime account maintenance already has an owner")
		}
	} else {
		if err != nil {
			return control, err
		}
		if !bytes.Equal(markerBytes, ownedMaintenance) {
			return control, errors.New("Runtime account maintenance changed")
		}
		coordinator, err := runtimeRegistrationCoordinator(root, registration)
		if err != nil {
			return control, err
		}
		var marker runtimeMaintenanceMarker
		if json.Unmarshal(markerBytes, &marker) != nil || marker.Schema != 1 ||
			marker.ID != coordinator || !runtimeInstanceID.MatchString(marker.Token) || marker.Phase != phase ||
			marker.ExpectedProfile != profile || marker.ExpectedVersion == "" ||
			marker.OwnerPID != os.Getpid() || marker.OwnerBirth != birth {
			return control, fmt.Errorf("Runtime account control requires the exact live %s owner", phase)
		}
		maintenance = map[string]any{
			"marker_sha256": controlDigest(markerBytes), "coordinator_id": coordinator,
			"controller_pid": marker.OwnerPID, "controller_birth": marker.OwnerBirth,
			"issuer_cli_version": marker.ExpectedVersion,
		}
	}
	issued := time.Now().UnixMilli()
	return runtimeAccountControl{
		binding: runtimeControlBinding{
			InstallationRoot: root, Profile: profile, LaunchID: grantID, ExpectedAccountID: expectedAccount,
		},
		image: image,
		port:  port,
		grant: map[string]any{
			"schema_version": 1, "protocol_version": 1, "nonce": grantID, "instance_id": grantID,
			"installation_root": root, "installation_id": controlDigest([]byte(root)), "profile": profile,
			"account_id": grantAccount, "sync_generation": 0, "image_path": image, "image_sha256": imageDigest,
			"supervisor_pid": os.Getpid(), "supervisor_birth": birth, "managed_id": nil,
			"registration_sha256": registrationDigest, "maintenance": maintenance, "port": port,
			"issued_at_ms": issued, "expires_at_ms": issued + 60_000,
		},
	}, nil
}

type runtimeAuthIdentity struct {
	Kind          string  `json:"kind"`
	AccountID     *string `json:"account_id,omitempty"`
	OperationID   string  `json:"operation_id,omitempty"`
	SessionDigest string  `json:"session_sha256,omitempty"`
}

// Bind account changes to the original bytes. Callers may pass nil only after
// confirming the session file is absent; malformed files cannot admit a login.
func runtimeAuthIdentityOf(original []byte) (runtimeAuthIdentity, error) {
	if original == nil {
		return runtimeAuthIdentity{Kind: "absent"}, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(original, &fields) != nil || fields == nil {
		return runtimeAuthIdentity{}, errors.New("Invalid Runtime account session")
	}
	text := func(name string) *string {
		var value *string
		if json.Unmarshal(fields[name], &value) != nil {
			return nil
		}
		return value
	}
	digest := controlDigest(original)
	if _, present := fields["user"]; present {
		user := text("user")
		if user == nil {
			return runtimeAuthIdentity{}, errors.New("Invalid Runtime session user")
		}
		if *user != "" {
			account := controlDigest([]byte(*user))
			return runtimeAuthIdentity{Kind: "bound", AccountID: &account, SessionDigest: digest}, nil
		}
	}

	for _, field := range []string{"kdf_salt", "wrapped_urk", "urk_nonce", "keyring_account", "keyring_backend", "auth_operation_id"} {
		if value := text(field); value == nil || *value == "" {
			return runtimeAuthIdentity{}, errors.New("Invalid unbound Runtime account session")
		}
	}
	var version int
	if json.Unmarshal(fields["vault_version"], &version) != nil {
		return runtimeAuthIdentity{}, errors.New("Invalid unbound Runtime vault version")
	}
	user, address := text("user"), text("addr")
	keygen := len(fields) == 7 && user == nil && version == 4
	offline := user != nil && *user == "" && address != nil && *address == "" && len(fields) == 9 && (version == 2 || version == 4)
	if version == 1 {
		backend := text("legacy_secret_backend")
		offline = user != nil && *user == "" && address != nil && *address == "" && len(fields) == 10 &&
			backend != nil && *backend != "" && *backend != "kernel"
	}
	if !keygen && !offline || *text("keyring_backend") == "kernel" {
		return runtimeAuthIdentity{}, errors.New("Unbound Runtime session is not a keygen or offline recovery candidate")
	}
	operationID := *text("auth_operation_id")
	if !runtimeInstanceID.MatchString(operationID) || operationID[14] != '4' || !strings.ContainsRune("89ab", rune(operationID[19])) {
		return runtimeAuthIdentity{}, errors.New("Invalid unbound Runtime account operation ID")
	}
	return runtimeAuthIdentity{Kind: "unbound", OperationID: operationID, SessionDigest: digest}, nil
}

func (m *Manager) readRuntimeLoginRecovery(ctx context.Context, owner HealthInfo, path string, original []byte) error {
	identity, err := runtimeAuthIdentityOf(original)
	if err != nil {
		return err
	}
	control, err := m.prepareRuntimeAccountControl(ctx, owner.Exe, owner.ImageSHA256, filepath.Dir(path), identity.AccountID, nil)
	if err != nil {
		return err
	}
	requestID, err := controlUUID()
	if err != nil {
		return err
	}
	params, err := json.Marshal(map[string]any{
		"schema_version": 1, "identity": identity, "binding": control.binding,
	})
	if err != nil {
		return err
	}
	request := runtimeControlRequest{V: 1, ID: requestID, Method: "control.auth.recovery_status", Args: []string{string(params)}}
	reply, err := m.exchangeRuntimeControl(ctx, control.image, control.port, control.grant, request)
	if err != nil {
		return err
	}
	var envelope struct {
		Command string `json:"command"`
		Status  string `json:"status"`
		Summary struct {
			State     string `json:"state"`
			Recovered *bool  `json:"recovered"`
		} `json:"summary"`
		Items []struct {
			Name, Status, Value string
		} `json:"items"`
		Errors []string `json:"errors"`
	}
	if json.Unmarshal(reply.Envelope, &envelope) != nil || envelope.Command != "login-recovery" ||
		envelope.Status != "ok" || len(envelope.Errors) != 0 || len(envelope.Items) != 2 ||
		envelope.Summary.Recovered == nil || *envelope.Summary.Recovered {
		return errors.New("Invalid Runtime login recovery receipt")
	}
	var ready, locked bool
	for _, item := range envelope.Items {
		ready = ready || item.Name == "control_ready" && item.Status == "ok" && item.Value == "true"
		locked = locked || item.Name == "store_ready" && item.Status == "skip" && item.Value == "false"
	}
	if !ready || !locked {
		return errors.New("Runtime did not confirm read-only recovery authorization")
	}
	if envelope.Summary.State != "clear" {
		return fmt.Errorf("Historical session requires login recovery first (%s)", envelope.Summary.State)
	}
	return nil
}

func (m *Manager) exchangeRuntimeControl(ctx context.Context, image string, port uint64, grant any, request runtimeControlRequest) (reply rpcResponse, err error) {
	frames := make([]byte, 0, 2*(4+controlFrameLimit))
	defer func() { clear(frames) }()
	for _, value := range []any{grant, request} {
		raw, err := json.Marshal(value)
		if err != nil {
			return reply, err
		}
		if len(raw) == 0 || len(raw) > controlFrameLimit {
			clear(raw)
			return reply, errors.New("Runtime control request exceeds its frame limit")
		}
		frames = binary.BigEndian.AppendUint32(frames, uint32(len(raw)))
		frames = append(frames, raw...)
		clear(raw)
	}
	cmd := exec.CommandContext(ctx, image, "--host-control-internal")
	cmd.Dir = filepath.Dir(image)
	configureCommand(cmd)
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		switch strings.ToUpper(name) {
		case "GH_TOKEN", "GITHUB_TOKEN", "RSRS_RUNTIME_BOOT_TOKEN", "RSRS_DATA_DIR", "RSRS_RPC_PORT",
			"RSRS_SUPER", "RSRS_PASS", "RSRS_SECRET", "RSRS_SECRET_KEY",
			"ONEMEMORY_SUPER", "ONEMEMORY_PASS", "ONEMEMORY_SECRET", "ONEMEMORY_SECRET_KEY",
			"RESPIRE_SUPER", "RESPIRE_PASS", "RESPIRE_SECRET", "RESPIRE_SECRET_KEY":
			continue
		}
		cmd.Env = append(cmd.Env, variable)
	}
	cmd.Env = append(cmd.Env, "RSRS_DATA_DIR="+m.DataRoot, "RSRS_RPC_PORT="+strconv.FormatUint(port, 10))
	input, err := cmd.StdinPipe()
	if err != nil {
		return reply, err
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		return reply, err
	}
	defer output.Close()
	if err := cmd.Start(); err != nil {
		return reply, fmt.Errorf("Start private Runtime control: %w", err)
	}
	waited := false
	defer func() {
		if !waited {
			killErr := cmd.Process.Kill()
			waitErr := cmd.Wait()
			if cmd.ProcessState == nil {
				err = errors.Join(err, fmt.Errorf("Runtime control child %d exit unconfirmed: %w", cmd.Process.Pid,
					errors.Join(killErr, waitErr)))
			}
		}
		if err != nil {
			err = errors.Join(err, ctx.Err())
			if request.OperationID != "" {
				err = &unconfirmedError{id: request.OperationID, cause: err}
			}
		}
	}()
	if _, err := bytes.NewReader(frames).WriteTo(input); err != nil {
		return reply, fmt.Errorf("Write Runtime control frames: %w", err)
	}
	var size uint32
	if err := binary.Read(output, binary.BigEndian, &size); err != nil {
		return reply, fmt.Errorf("Read Runtime control frame: %w", err)
	}
	if size == 0 || size > controlFrameLimit {
		return reply, errors.New("Invalid Runtime control response frame size")
	}
	frame := make([]byte, size)
	defer clear(frame)
	if _, err := io.ReadFull(output, frame); err != nil {
		return reply, fmt.Errorf("Read Runtime control reply: %w", err)
	}
	var tail [1]byte
	if n, err := output.Read(tail[:]); n != 0 || err != io.EOF {
		return reply, errors.New("Unexpected data after Runtime control reply")
	}
	waitErr := cmd.Wait()
	waited = true
	var packet struct {
		rpcResponse
		PID            int     `json:"pid"`
		Binary         string  `json:"bin"`
		IdempotencyKey string  `json:"idempotency_key"`
		WebURL         *string `json:"web_url"`
	}
	if json.Unmarshal(frame, &packet) != nil || packet.V != request.V || packet.ID != request.ID ||
		packet.OperationID != request.OperationID || packet.IdempotencyKey != request.IdempotencyKey ||
		packet.PID != cmd.Process.Pid || packet.Binary != "rsrs-runtime" || packet.Scope != nil || packet.WebURL != nil ||
		cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != packet.Exit {
		return reply, errors.New("Runtime control reply or process exit does not match the request")
	}
	reply = packet.rpcResponse
	if !reply.OK || reply.Exit != 0 || reply.Code != "" || reply.Error != "" {
		return reply, fmt.Errorf("Runtime rejected private control request (%s)", reply.Code)
	}
	return reply, waitErr
}
