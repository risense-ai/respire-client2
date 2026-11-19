package host

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

func TestLinuxSecretServiceInteroperability(t *testing.T) {
	if os.Getenv("RESPIRE_NATIVE_SECRET_SERVICE_CHECK") != "1" {
		t.Skip("run the native release check inside dbus-run-session")
	}
	if !strings.HasPrefix(os.Getenv("DBUS_SESSION_BUS_ADDRESS"), "unix:") {
		t.Fatal("the native release check requires a private Unix session bus")
	}
	bus, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	daemonBus := bus.BusObject()
	var owned bool
	if err := daemonBus.Call("org.freedesktop.DBus.NameHasOwner", 0, secretService).Store(&owned); err != nil || owned {
		t.Fatalf("the test bus must have no existing Secret Service owner: %v", err)
	}
	home := t.TempDir()
	control := filepath.Join(home, "control")
	if err := os.Mkdir(control, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_RUNTIME_DIR", control)
	t.Setenv("GNOME_KEYRING_CONTROL", control)
	t.Setenv("GNOME_KEYRING_PID", "")
	t.Setenv("SSH_AUTH_SOCK", "")
	var stderr bytes.Buffer
	service := exec.Command("gnome-keyring-daemon", "--foreground", "--unlock", "--components=secrets", "--control-directory", control)
	service.Stdin = strings.NewReader("isolated release fixture password")
	service.Stderr = &stderr
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = service.Process.Kill()
		_ = service.Wait()
		if t.Failed() {
			t.Logf("isolated Secret Service: %s", stderr.String())
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for {
		if err := daemonBus.CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, secretService).Store(&owned); err != nil {
			t.Fatal(err)
		}
		if owned {
			var pid uint32
			if err := daemonBus.CallWithContext(ctx, "org.freedesktop.DBus.GetConnectionUnixProcessID", 0, secretService).Store(&pid); err != nil || pid != uint32(service.Process.Pid) {
				t.Fatalf("unexpected Secret Service owner: %v", err)
			}
			var collection dbus.ObjectPath
			if err := bus.Object(secretService, secretRoot).CallWithContext(ctx, secretAPI+"Service.ReadAlias", 0, "default").Store(&collection); err != nil {
				t.Fatal(err)
			}
			if collection != "/" {
				break
			}
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("isolated Secret Service default collection never appeared")
		}
	}
	const legacy = "historical-钥匙"
	store := exec.CommandContext(ctx, "secret-tool", "store", "--label", "old Respire slot", "service", "rsrs", "username", "pass:old")
	store.Stdin = strings.NewReader(legacy)
	if output, err := store.CombinedOutput(); err != nil {
		t.Fatalf("seed target-less credential: %v: %s", err, output)
	}
	read, err := secretServiceCredential(ctx, "rsrs", "pass:old", nil)
	if err != nil || read == nil || *read != legacy {
		t.Fatalf("read historical slot: %v", err)
	}
	for _, value := range []string{"migrated-凭据", ""} {
		account := fmt.Sprintf("legacy-secret:fixture-%d", len(value))
		if _, err := secretServiceCredential(ctx, "rsrs", account, &value); err != nil {
			t.Fatal(err)
		}
		wrong := "different credential"
		if _, err := secretServiceCredential(ctx, "rsrs", account, &wrong); err == nil {
			t.Fatal("conflicting entry was accepted")
		}
		lookup := exec.CommandContext(ctx, "secret-tool", "lookup", "service", "rsrs", "username",
			account, "target", "default", "application", "rust-keyring")
		output, err := lookup.Output()
		if err != nil || strings.TrimSuffix(string(output), "\n") != value {
			t.Fatalf("native lookup of migrated slot failed: %v", err)
		}
	}
	const custody = "login-control:login-0123456789abcdef0123456789abcdef"
	original := `{"state":"challenge"}`
	if _, err := secretServiceCredential(ctx, "rsrs", custody, &original); err != nil {
		t.Fatal(err)
	}
	verified := `{"state":"verified","fixture":"密钥"}`
	if _, err := updateSecretServiceCredential(ctx, "rsrs", custody, &original, &verified); err != nil {
		t.Fatal(err)
	}
	if _, err := updateSecretServiceCredential(ctx, "rsrs", custody, &original, &original); err == nil {
		t.Fatal("stale custody update would replace the verified value")
	}
	lookup := exec.CommandContext(ctx, "secret-tool", "lookup", "service", "rsrs", "username",
		custody, "target", "default", "application", "rust-keyring")
	output, err := lookup.Output()
	if err != nil || strings.TrimSuffix(string(output), "\n") != verified {
		t.Fatalf("native lookup of updated custody failed: %v", err)
	}
	if err := removeSecretServiceCredential(ctx, "rsrs", custody, original); err == nil {
		t.Fatal("stale cleanup would delete the verified credential")
	}
	if err := removeSecretServiceCredential(ctx, "rsrs", custody, verified); err != nil {
		t.Fatal(err)
	}
	if value, err := secretServiceCredential(ctx, "rsrs", custody, nil); err != nil || value != nil {
		t.Fatalf("temporary login credential was not removed: %v", err)
	}
}
