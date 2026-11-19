//go:build linux || windows || (darwin && cgo)

package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
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

func controlUUID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]), nil
}

func controlDigest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func (m *Manager) readRuntimeLoginRecovery(ctx context.Context, owner HealthInfo, path string, original []byte, session runtimeSession) error {
	root, err := canonicalSessionDirectory(m.DataRoot)
	if err != nil {
		return err
	}
	profile, err := canonicalSessionDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	image, err := canonicalSessionDirectory(owner.Exe)
	if err != nil {
		return err
	}
	actualImage, err := fileHash(image)
	if err != nil {
		return err
	}
	if actualImage != owner.ImageSHA256 {
		return errors.New("Runtime control image changed")
	}
	address, err := url.Parse(m.Endpoint)
	if err != nil {
		return err
	}
	port, err := strconv.ParseUint(address.Port(), 10, 16)
	if err != nil || port == 0 {
		return errors.New("Invalid Runtime control port")
	}
	birth, err := hostControlBirth(ctx)
	if err != nil {
		return err
	}
	grantID, err := controlUUID()
	if err != nil {
		return err
	}
	requestID, err := controlUUID()
	if err != nil {
		return err
	}
	identity := map[string]any{"session_sha256": controlDigest(original)}
	grantAccount := controlDigest([]byte(profile))
	var expectedAccount any
	if session.User != "" {
		grantAccount = controlDigest([]byte(session.User))
		expectedAccount = grantAccount
		identity["kind"] = "bound"
		identity["account_id"] = grantAccount
	} else {
		var unbound struct {
			OperationID string `json:"auth_operation_id"`
		}
		if json.Unmarshal(original, &unbound) != nil || !runtimeInstanceID.MatchString(unbound.OperationID) {
			return errors.New("Historical session has no identity for Runtime login recovery")
		}
		identity["kind"] = "unbound"
		identity["operation_id"] = unbound.OperationID
	}
	var registrationDigest any
	registration, err := readCredentialConfig(filepath.Join(m.DataRoot, "runtime", "autostart", "registration.json"), controlFrameLimit)
	if err == nil {
		registrationDigest = controlDigest(registration)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	issued := time.Now().UnixMilli()
	grant := map[string]any{
		"schema_version": 1, "protocol_version": 1, "nonce": grantID, "instance_id": grantID,
		"installation_root": root, "installation_id": controlDigest([]byte(root)), "profile": profile,
		"account_id": grantAccount, "sync_generation": 0, "image_path": image, "image_sha256": owner.ImageSHA256,
		"supervisor_pid": os.Getpid(), "supervisor_birth": birth, "managed_id": nil,
		"registration_sha256": registrationDigest, "maintenance": nil, "port": port,
		"issued_at_ms": issued, "expires_at_ms": issued + 60_000,
	}
	params, err := json.Marshal(map[string]any{
		"schema_version": 1, "identity": identity,
		"binding": map[string]any{"installation_root": root, "profile": profile, "launch_id": grantID, "expected_account_id": expectedAccount},
	})
	if err != nil {
		return err
	}
	request := map[string]any{"v": 1, "id": requestID, "method": "control.auth.recovery_status", "args": []string{string(params)}}
	reply, err := m.exchangeRuntimeControl(ctx, owner.Exe, port, grant, request, requestID)
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

func (m *Manager) exchangeRuntimeControl(ctx context.Context, image string, port uint64, grant, request any, id string) (reply rpcResponse, err error) {
	var frames bytes.Buffer
	for _, value := range []any{grant, request} {
		raw, err := json.Marshal(value)
		if err != nil {
			return reply, err
		}
		if len(raw) == 0 || len(raw) > controlFrameLimit {
			return reply, errors.New("Runtime control request exceeds its frame limit")
		}
		frames.Write(binary.BigEndian.AppendUint32(nil, uint32(len(raw))))
		frames.Write(raw)
	}
	defer func(data []byte) { clear(data) }(frames.Bytes())
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
			_ = input.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	if _, err := frames.WriteTo(input); err != nil {
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
		PID int `json:"pid"`
	}
	if json.Unmarshal(frame, &packet) != nil || packet.V != 1 || packet.ID != id || packet.PID != cmd.Process.Pid ||
		cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != packet.Exit {
		return reply, errors.New("Runtime control reply or process exit does not match the request")
	}
	reply = packet.rpcResponse
	if !reply.OK || reply.Exit != 0 || reply.Code != "" || reply.Error != "" {
		return reply, fmt.Errorf("Runtime rejected private control request (%s)", reply.Code)
	}
	return reply, waitErr
}
