//go:build windows && native_selfupdate_test

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"respire-client2/internal/host"
)

func TestNativeDesktopSelfUpdate(t *testing.T) {
	const oldVersion, newVersion = "0.0.1-dev.1", "0.0.1-dev.2"
	runtimePort := reserveRefusedRuntimePort(t)
	buildDir := t.TempDir()
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, edit := range [][2]string{
		{`"respire-client2/internal/host"`, "\"respire-client2/internal/host\"\n\"respire-client2/testdata/selfupdate\""},
		{"desktop.Run(m, version)", "selfupdate.Attach(m, version)\ndesktop.Run(m, version)"},
	} {
		if bytes.Count(source, []byte(edit[0])) != 1 {
			t.Fatalf("expected one overlay anchor %q", edit[0])
		}
		source = bytes.Replace(source, []byte(edit[0]), []byte(edit[1]), 1)
	}
	replacement := filepath.Join(buildDir, "main.go")
	writeSelfUpdateFixture(t, replacement, source)
	mainPath, err := filepath.Abs("main.go")
	if err != nil {
		t.Fatal(err)
	}
	overlay, err := json.Marshal(map[string]any{"Replace": map[string]string{mainPath: replacement}})
	if err != nil {
		t.Fatal(err)
	}
	overlayPath := filepath.Join(buildDir, "overlay.json")
	writeSelfUpdateFixture(t, overlayPath, overlay)
	build := func(version string) []byte {
		t.Helper()
		path := filepath.Join(buildDir, version+".exe")
		cmd := exec.Command("go", "build", "-tags", "production", "-overlay", overlayPath, "-ldflags", "-X main.version="+version, "-o", path, ".")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("compile %s: %v\n%s", version, err, output)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	oldEXE, newEXE := build(oldVersion), build(newVersion)
	digest := sha256.Sum256(newEXE)
	asset := "respire-windows-" + runtime.GOARCH + ".exe"
	manifest := fmt.Sprintf(`{"schemaVersion":1,"dev":{"version":%q,"artifacts":[{"filename":%q,"size":%d,"sha256":%q}]}}`,
		newVersion, asset, len(newEXE), base64.StdEncoding.EncodeToString(digest[:]))
	const prefix = "/https://github.com/risense-ai/respire-client2/releases/"
	manifestPath := prefix + "latest/download/client-updates.json"
	assetPath := prefix + "download/v" + newVersion + "/" + asset

	for _, selection := range []string{"existing", "missing"} {
		t.Run(selection, func(t *testing.T) {
			requests := make(chan string, 16)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- r.Header.Get("X-Test-Client-Version") + " " + r.URL.Path
				switch r.URL.Path {
				case manifestPath:
					fmt.Fprint(w, manifest)
				case assetPath:
					w.Write(newEXE)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)

			root := t.TempDir()
			data := filepath.Join(root, "data")
			exe := filepath.Join(root, "client2.exe")
			prefs := filepath.Join(data, "client2", "preferences.json")
			cli := filepath.Join(data, "bin", "rsrs.exe")
			selected := filepath.Join(data, "bin", "runtime.json")
			state := map[string][]byte{
				prefs: []byte(`{"clientChannel":"dev","runtimeChannel":"stable","cliChannel":"dev","autoRuntime":true,"clientAutoUpdate":true,"clientAutoRestart":true,"runtimeAutoUpdate":true,"runtimeUpdateInterval":"5m","proxyMode":"direct","onboardingCompleted":true}`),
				cli:   []byte("synthetic optional CLI, never executable"),
			}
			if selection == "existing" {
				// An identity without an installation receipt exercises startup verification
				// without launching a Runtime or using account material.
				state[selected] = []byte(fmt.Sprintf(`{"version":"2.0.0-dev.28","sha256":%q}`, strings.Repeat("a", 64)))
			}
			for path, contents := range state {
				writeSelfUpdateFixture(t, path, contents)
			}
			writeSelfUpdateFixture(t, exe, oldEXE)

			cmd := exec.Command(exe, "--tray")
			cmd.Dir = root
			cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
			for _, key := range []string{"SystemRoot", "SystemDrive", "WINDIR", "ProgramFiles", "ProgramFiles(x86)", "ProgramW6432", "ProgramData"} {
				cmd.Env = append(cmd.Env, key+"="+os.Getenv(key))
			}
			systemRoot := os.Getenv("SystemRoot")
			system32 := filepath.Join(systemRoot, "System32")
			cmd.Env = append(cmd.Env, "PATH="+strings.Join([]string{system32, systemRoot,
				filepath.Join(system32, "WindowsPowerShell", "v1.0")}, string(os.PathListSeparator)))
			cmd.Env = append(cmd.Env, "HOME="+root, "USERPROFILE="+root, "APPDATA="+root,
				"LOCALAPPDATA="+root, "TEMP="+root, "TMP="+root, "RSRS_DATA_DIR="+data,
				"RSRS_RPC_PORT="+runtimePort,
				fmt.Sprintf("RSRS_INSTANCE_ID=selfupdate-%d-%s", os.Getpid(), selection),
				"CLIENT2_NATIVE_UPDATE_FIXTURE="+server.Listener.Addr().String())
			t.Cleanup(func() {
				if t.Failed() && cmd.Process != nil {
					for _, path := range []string{filepath.Join(data, "client2", "client.log"), filepath.Join(root, fmt.Sprintf("wails-update-%d.log", cmd.Process.Pid))} {
						if output, err := os.ReadFile(path); err == nil {
							t.Logf("%s:\n%s", filepath.Base(path), output)
						}
					}
				}
			})
			wait := startSelfUpdateFixture(t, cmd)

			deadline := time.After(75 * time.Second)
			for _, want := range []string{oldVersion + " " + manifestPath, oldVersion + " " + assetPath, newVersion + " " + manifestPath} {
				select {
				case got := <-requests:
					if got != want {
						t.Fatalf("request %q, want %q", got, want)
					}
				case <-deadline:
					t.Fatalf("waiting for %q", want)
				}
			}
			if err := <-wait; err != nil {
				t.Fatalf("original Client2 exit: %v", err)
			}
			installed, err := os.ReadFile(exe)
			if err != nil || sha256.Sum256(installed) != digest {
				t.Fatalf("replacement image does not match the verified download: %v", err)
			}

			// The relaunched process checks the Client2 manifest before it would
			// enter the Runtime update branch of the same background iteration.
			select {
			case got := <-requests:
				t.Fatalf("unexpected request after Client2 relaunch: %q", got)
			case <-time.After(2 * time.Second):
			}
			for path, want := range state {
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("self-update changed %s: %v", filepath.Base(path), err)
				}
			}
			if selection == "missing" {
				if _, err := os.Stat(selected); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("self-update must leave missing Runtime selection absent: %v", err)
				}
			}
		})
	}
}

func reserveRefusedRuntimePort(t *testing.T) string {
	t.Helper()
	socket, err := windows.Socket(windows.AF_INET, windows.SOCK_STREAM, windows.IPPROTO_TCP)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { windows.Closesocket(socket) })
	// Reserve the address exclusively without listening. This gives Client2
	// a real connection refusal while preventing another local listener.
	const exclusiveAddressUse = ^windows.SO_REUSEADDR
	if err := windows.SetsockoptInt(socket, windows.SOL_SOCKET, exclusiveAddressUse, 1); err != nil {
		t.Fatal(err)
	}
	if err := windows.Bind(socket, &windows.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}
	address, err := windows.Getsockname(socket)
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(address.(*windows.SockaddrInet4).Port)
	connection, err := net.DialTimeout("tcp4", "127.0.0.1:"+port, time.Second)
	if connection != nil {
		connection.Close()
	}
	if !host.IsUnavailable(err) {
		t.Fatalf("reserved Runtime port must refuse connections: %v", err)
	}
	return port
}

func writeSelfUpdateFixture(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatal(err)
	}
}

func startSelfUpdateFixture(t *testing.T, cmd *exec.Cmd) <-chan error {
	t.Helper()
	// No kill-on-close flag: Wails keeps the helper in a job with no breakaway
	// flags. Explicit termination reaps the helper and the relaunched app.
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		windows.CloseHandle(job)
		t.Fatal(err)
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	t.Cleanup(func() {
		windows.TerminateJobObject(job, 1)
		windows.CloseHandle(job)
		cmd.Process.Kill()
	})
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		t.Fatal(err)
	}
	return wait
}
