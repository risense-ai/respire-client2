package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The copied test executable exercises the real host process boundary without a reset service.
func TestMain(m *testing.M) {
	if os.Getenv("RESPIRE_TEST_RECOVERY_CLI") != "1" {
		os.Exit(m.Run())
	}
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("rsrs 1.0.18")
		os.Exit(0)
	}
	args, err := json.Marshal(os.Args[1:])
	if err == nil {
		var log *os.File
		log, err = os.OpenFile(os.Getenv("RESPIRE_TEST_RECOVERY_CALLS"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err == nil {
			_, err = log.Write(append(args, '\n'))
			if closeErr := log.Close(); err == nil {
				err = closeErr
			}
		}
	}
	var raw []byte
	if err == nil {
		raw, err = os.ReadFile(os.Getenv("RESPIRE_TEST_RECOVERY_REPLY"))
	}
	if err != nil {
		os.Exit(1)
	}
	if len(os.Args) == 3 && os.Args[1] == "--json" && os.Args[2] == "--host-rpc-stdio" {
		var request struct {
			ID     string `json:"id"`
			Method string `json:"method"`
		}
		if json.NewDecoder(os.Stdin).Decode(&request) != nil || request.Method != "auth.reset" {
			os.Exit(1)
		}
		if json.NewEncoder(os.Stdout).Encode(map[string]any{"v": 1, "id": request.ID, "ok": true, "envelope": json.RawMessage(raw)}) != nil {
			os.Exit(1)
		}
	} else {
		if len(os.Args) != 6 || os.Args[1] != "--json" || os.Args[2] != "runtime" || os.Args[3] != "key-result" {
			os.Exit(1)
		}
		if _, err := os.Stdout.Write(raw); err != nil {
			os.Exit(1)
		}
	}
	os.Exit(0)
}

func recoveryMaterialFixture(t *testing.T) (*Manager, string, string, string) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, cliBinary()), image, 0700); err != nil {
		t.Fatal(err)
	}
	reply, calls := filepath.Join(root, "reply.json"), filepath.Join(root, "calls.jsonl")
	t.Setenv("RESPIRE_TEST_RECOVERY_CLI", "1")
	t.Setenv("RESPIRE_TEST_RECOVERY_REPLY", reply)
	t.Setenv("RESPIRE_TEST_RECOVERY_CALLS", calls)
	id := "desktop-20000000-0000-4000-8000-000000000001"
	m := &Manager{Root: filepath.Join(root, "client"), DataRoot: root, Endpoint: "http://127.0.0.1:9", jobs: map[string]*Operation{}}
	if err := os.MkdirAll(m.Root, 0700); err != nil {
		t.Fatal(err)
	}
	return m, id, reply, calls
}

func resetMaterialEnvelope(t *testing.T, m *Manager, outcome string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"status": "ok", "summary": map[string]any{
		"operation_id": "20000000-0000-4000-8000-000000000002", "source_profile": m.DataRoot,
		"outcome": outcome, "super": "synthetic-issued-recovery-material",
	}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestResetMaterialSurvivesRestartUntilExplicitSave(t *testing.T) {
	m, id, reply, calls := recoveryMaterialFixture(t)
	if err := os.WriteFile(reply, resetMaterialEnvelope(t, m, "committed"), 0600); err != nil {
		t.Fatal(err)
	}
	m.jobs[id] = &Operation{ID: id, Name: "auth_reset", State: "running", Started: time.Now(), host: true, write: true}
	spec, ok := operationSpec("auth_reset")
	if !ok {
		t.Fatal("reset command missing")
	}
	m.runOperation(spec, nil, "{}", id)
	job, err := m.Operation(id)
	if err != nil || job.State != "done" || !job.MaterialPending || job.MaterialSaved {
		t.Fatal("committed reset must await material save", err)
	}
	original := *job.Transaction
	if err := m.Acknowledge(id); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Continue(id, "desktop-20000000-0000-4000-8000-000000000003", json.RawMessage(`{}`)); err == nil {
		t.Fatal("committed reset cannot be continued as input")
	}
	assertJournal := func() {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(m.Root, "requests.json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"synthetic-issued-recovery-material", "super", "envelope", "materialPending"} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatal("journal contains recovery material or transient receipt fields")
			}
		}
	}
	assertJournal()
	for restart := 0; restart < 2; restart++ {
		m = &Manager{Root: m.Root, DataRoot: m.DataRoot, Endpoint: m.Endpoint}
		if err := m.loadOperations(); err != nil {
			t.Fatal(err)
		}
		listed := m.Operations()
		if len(listed) != 1 || !listed[0].MaterialPending || len(listed[0].Envelope) != 0 || *listed[0].Transaction != original {
			t.Fatal("restart lost pending material or original identity")
		}
		if err := m.AcknowledgeRecoveryMaterial(id); err == nil {
			t.Fatal("must query and display original material before saving")
		}
		job, err = m.Recover(id)
		if err != nil || job.State != "done" || !job.MaterialPending || *job.Transaction != original || !strings.Contains(string(job.Envelope), "synthetic-issued-recovery-material") {
			t.Fatal("original material was not recovered", err)
		}
		assertJournal()
	}
	if err := m.AcknowledgeRecoveryMaterial(id); err != nil {
		t.Fatal(err)
	}
	if err := m.AcknowledgeRecoveryMaterial(id); err != nil {
		t.Fatal("save acknowledgement must be idempotent", err)
	}
	assertJournal()
	if err := m.loadOperations(); err != nil {
		t.Fatal(err)
	}
	job, err = m.Recover(id)
	if err != nil || job.MaterialPending || !job.MaterialSaved || *job.Transaction != original {
		t.Fatal("saved material must stay acknowledged after restart", err)
	}
	logged, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(logged)), "\n")
	if len(lines) != 3 || strings.Count(string(logged), "--host-rpc-stdio") != 1 || strings.Count(string(logged), "key-result") != 2 || strings.Contains(string(logged), "synthetic-issued-recovery-material") {
		t.Fatal("recovery repeated reset, queried after save, or logged material")
	}
	for _, line := range lines[1:] {
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatal(err)
		}
		if len(args) != 5 || args[3] != "--operation-id="+original.OperationID || args[4] != "--source-profile="+original.SourceProfile {
			t.Fatal("query changed original identity")
		}
	}
}

func TestResetMaterialSaveFailureRetainsPendingReceipt(t *testing.T) {
	m, id, _, _ := recoveryMaterialFixture(t)
	raw := resetMaterialEnvelope(t, m, "committed")
	m.jobs[id] = &Operation{ID: id, Name: "auth_reset", State: "done", host: true, write: true,
		Envelope: raw, Transaction: transactionFromEnvelope("auth_reset", raw)}
	// Make the journal destination unwritable without relying on platform permissions.
	if err := os.Mkdir(filepath.Join(m.Root, "requests.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := m.AcknowledgeRecoveryMaterial(id); err == nil {
		t.Fatal("a failed durable save must be reported")
	}
	job, err := m.Operation(id)
	if err != nil || !job.MaterialPending || job.MaterialSaved {
		t.Fatal("a failed save discarded the pending acknowledgement", err)
	}
}

func TestResetMaterialRecoveryPreservesFailuresAndIdentity(t *testing.T) {
	for _, outcome := range []string{"unknown", "rolled_back", "foreign", "missing_material"} {
		t.Run(outcome, func(t *testing.T) {
			m, id, reply, _ := recoveryMaterialFixture(t)
			original := resetMaterialEnvelope(t, m, "committed")
			m.jobs[id] = &Operation{ID: id, Name: "auth_reset", State: "done", host: true, write: true, Transaction: transactionFromEnvelope("auth_reset", original)}
			raw := resetMaterialEnvelope(t, m, outcome)
			if outcome == "foreign" {
				raw = []byte(strings.Replace(string(original), "20000000-0000-4000-8000-000000000002", "20000000-0000-4000-8000-000000000003", 1))
			}
			if outcome == "missing_material" {
				raw = []byte(strings.Replace(string(original), "synthetic-issued-recovery-material", "", 1))
			}
			if err := os.WriteFile(reply, raw, 0600); err != nil {
				t.Fatal(err)
			}
			identity := *m.jobs[id].Transaction
			job, err := m.Recover(id)
			if err != nil {
				t.Fatal(err)
			}
			want := "done"
			if outcome == "unknown" || outcome == "foreign" {
				want = "unconfirmed"
			}
			if outcome == "rolled_back" {
				want = "failed"
			}
			if job.State != want || *job.Transaction != identity || job.MaterialSaved {
				t.Fatal("recovery changed failure semantics or identity")
			}
			if err := m.AcknowledgeRecoveryMaterial(id); err == nil {
				t.Fatal("unverified recovery material was acknowledged")
			}
		})
	}
}

func TestPendingResetMaterialCannotBeEvicted(t *testing.T) {
	m := &Manager{jobs: map[string]*Operation{}}
	for n := 0; n < 128; n++ {
		id := fmt.Sprintf("desktop-20000000-0000-4000-8000-%012d", n)
		m.jobs[id] = &Operation{ID: id, Name: "auth_reset", State: "done", host: true, Transaction: &hostTransaction{Kind: "key"}}
	}
	if _, err := m.Submit("auth_reset", "desktop-30000000-0000-4000-8000-000000000001", json.RawMessage(`{}`), true, "{}"); err == nil {
		t.Fatal("must not evict unacknowledged material")
	}
	if len(m.jobs) != 128 {
		t.Fatal("pending reset record was evicted")
	}
}

func TestStartTaskCancellablePublishesBusyBeforeReturning(t *testing.T) {
	m := &Manager{task: TaskState{Stage: "就绪"}}
	release := make(chan struct{})
	started := make(chan struct{})
	if !m.StartTaskCancellable("下载模型 quantized", func(ctx context.Context) error {
		close(started)
		<-release
		return nil
	}) {
		t.Fatal("first start must succeed")
	}
	// The busy state must be visible synchronously after Start returns: this
	// is what lets a poller trust the snapshot instead of a stale pre-start
	// terminal state from a previous task.
	if snap := m.TaskSnapshot(); !snap.Busy || snap.Stage != "下载模型 quantized" {
		t.Fatalf("snapshot after start = %+v, want busy with stage set", snap)
	}
	if m.StartTaskCancellable("另一个任务", func(ctx context.Context) error { return nil }) {
		t.Fatal("second start while busy must be rejected")
	}
	// A rejected start must not clobber the running task's progress state.
	if snap := m.TaskSnapshot(); !snap.Busy || snap.Stage != "下载模型 quantized" {
		t.Fatalf("rejected start must leave the running task untouched: %+v", snap)
	}
	close(release)
	<-started
	waitTaskIdle(t, m)
	snap := m.TaskSnapshot()
	if snap.Stage != "完成" || snap.Error != "" {
		t.Fatalf("finished task = %+v, want 完成 without error", snap)
	}
}

func TestStartTaskCancellableReportsFailure(t *testing.T) {
	m := &Manager{task: TaskState{Stage: "就绪"}}
	if !m.StartTaskCancellable("失败任务", func(ctx context.Context) error {
		return errors.New("下载源不可达")
	}) {
		t.Fatal("start must succeed")
	}
	waitTaskIdle(t, m)
	snap := m.TaskSnapshot()
	if snap.Stage != "操作失败" || snap.Error != "下载源不可达" {
		t.Fatalf("failed task = %+v, want 操作失败 with error kept", snap)
	}
}

func waitTaskIdle(t *testing.T, m *Manager) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for m.TaskSnapshot().Busy {
		if time.Now().After(deadline) {
			t.Fatal("task did not finish in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
