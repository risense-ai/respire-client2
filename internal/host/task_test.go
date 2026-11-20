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
	if len(os.Args) == 3 && os.Args[1] == "--json" && os.Args[2] == "capabilities" {
		methods := []string{"core_config.set", "core_config.result"}
		unsupported := os.Getenv("RESPIRE_TEST_CORE_UNSUPPORTED")
		if unsupported == "once" {
			marker := os.Getenv("RESPIRE_TEST_RECOVERY_CALLS") + ".capability"
			if _, err := os.Stat(marker); errors.Is(err, os.ErrNotExist) {
				if err := os.WriteFile(marker, nil, 0600); err != nil {
					os.Exit(1)
				}
				unsupported = "1"
			}
		}
		if unsupported == "1" {
			methods = nil
		} else if unsupported == "result" {
			methods = methods[:1]
		}
		status, exit := "ok", 0
		switch unsupported {
		case "warn":
			status, methods = "warn", nil
		case "warn-supported":
			status = "warn"
		case "nonzero":
			methods, exit = nil, 7
		case "nonzero-supported":
			exit = 7
		case "malformed":
			fmt.Print("invalid capabilities JSON")
			os.Exit(0)
		}
		if json.NewEncoder(os.Stdout).Encode(map[string]any{"status": status, "summary": map[string]any{"capabilities": map[string]any{"host_rpc": map[string]any{"version": 1, "transport": "stdio", "methods": methods}}}}) != nil {
			os.Exit(1)
		}
		os.Exit(exit)
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
		if json.NewDecoder(os.Stdin).Decode(&request) != nil || (request.Method != "auth.reset" && request.Method != "core_config.set" && request.Method != "core_config.result") {
			os.Exit(1)
		}
		if request.Method == "core_config.set" && os.Getenv("RESPIRE_TEST_CORE_DROP_REPLY") == "1" {
			os.Exit(1)
		}
		if json.NewEncoder(os.Stdout).Encode(map[string]any{"v": 1, "id": request.ID, "ok": true, "envelope": json.RawMessage(raw)}) != nil {
			os.Exit(1)
		}
	} else {
		if len(os.Args) != 6 || os.Args[1] != "--json" || os.Args[2] != "runtime" || (os.Args[3] != "key-result" && os.Args[3] != "core-config-result") {
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

func TestHistoricalResetMaterialSurvivesRestartUntilExplicitSave(t *testing.T) {
	m, id, reply, calls := recoveryMaterialFixture(t)
	if err := os.WriteFile(reply, resetMaterialEnvelope(t, m, "committed"), 0600); err != nil {
		t.Fatal(err)
	}
	legacy, err := m.hostOperation(context.Background(), nil, "{}", "auth.reset", id)
	if err != nil {
		t.Fatal(err)
	}
	m.jobs[id] = &Operation{ID: id, Name: "auth_reset", State: "done", Started: time.Now(), host: true, write: true,
		Envelope: legacy, Transaction: transactionFromEnvelope("auth_reset", legacy)}
	if err := m.persistOperationsLocked(); err != nil {
		t.Fatal(err)
	}
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
	for _, outcome := range []string{"warn", "warn-supported", "nonzero", "nonzero-supported", "malformed"} {
		t.Run("capability_probe_"+outcome, func(t *testing.T) {
			m, id, _, calls := recoveryMaterialFixture(t)
			t.Setenv("RESPIRE_TEST_CORE_UNSUPPORTED", outcome)
			if err := m.RequireCoreConfigHost(context.Background()); err == nil || errors.Is(err, errHostMethodUnsupported) {
				t.Fatal("failed probe was treated as a missing method", err)
			}
			if _, err := os.Stat(filepath.Join(m.DataRoot, "bin", "runtime", ".install.lock")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed probe entered automatic installation", err)
			}
			for _, method := range []string{"core_config.set", "core_config.result"} {
				if _, err := m.hostOperation(context.Background(), nil, "", method, id); err == nil || errors.Is(err, errHostMethodUnsupported) {
					t.Fatal("failed probe allowed RPC or reported missing method", method, err)
				}
			}
			if _, err := os.Stat(calls); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed probe sent a business request", err)
			}
		})
	}
	for _, method := range []string{"core_config.set", "core_config.result"} {
		t.Run("unsupported_"+method, func(t *testing.T) {
			m, id, _, calls := recoveryMaterialFixture(t)
			t.Setenv("RESPIRE_TEST_CORE_UNSUPPORTED", "result")
			_, err := m.hostOperation(context.Background(), nil, "", method, id)
			if !errors.Is(err, errHostMethodUnsupported) || !strings.Contains(err.Error(), "core_config.result") {
				t.Fatal("missing capability was not rejected", err)
			}
			if _, err := os.Stat(calls); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unsupported method sent a business request", err)
			}
		})
	}
	t.Run("historical_cli_core_config_entire_reply_lost", func(t *testing.T) {
		m, id, reply, calls := recoveryMaterialFixture(t)
		t.Setenv("RESPIRE_TEST_CORE_DROP_REPLY", "1")
		full := hostTransaction{OperationID: strings.TrimPrefix(id, "desktop-"), Kind: "core_config",
			SourceProfile: filepath.Join(m.DataRoot, "profiles", "actual"), Scope: runtimeScope{"synthetic", "original", 1}}
		raw, err := json.Marshal(map[string]any{"status": "ok", "details": full,
			"summary": map[string]any{"core_application": map[string]any{"state": "applied"}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(reply, raw, 0600); err != nil {
			t.Fatal(err)
		}
		binding := hostTransaction{OperationID: strings.TrimPrefix(id, "desktop-"), Kind: "core_config", RequestBound: true}
		m.jobs[id] = &Operation{ID: id, Name: "agent_set", State: "running", Started: time.Now(), host: true, write: true, Transaction: &binding}
		if err := m.persistOperationsLocked(); err != nil {
			t.Fatal(err)
		}
		legacy, _ := operationSpec("agent_set")
		legacy.method = "core_config.set"
		go m.runOperation(legacy, []string{"agent-config", `--set=recall_fusion={"fusion":"legacy"}`}, "", id)
		var job Operation
		deadline := time.Now().Add(10 * time.Second)
		for {
			job, err = m.Operation(id)
			if err != nil {
				t.Fatal(err)
			}
			if job.State != "running" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("host reply-loss test timed out")
			}
			time.Sleep(time.Millisecond)
		}
		if job.State != "unconfirmed" || job.Transaction == nil || !job.Transaction.RequestBound || job.Transaction.SourceProfile != "" {
			t.Fatal("missing reply erased prebound identity", job.State, job.Error, job.Transaction)
		}
		restarted := &Manager{Root: m.Root, DataRoot: m.DataRoot, Endpoint: m.Endpoint}
		if err := restarted.loadOperations(); err != nil {
			t.Fatal(err)
		}
		t.Setenv("RESPIRE_TEST_CORE_UNSUPPORTED", "1")
		job, err = restarted.Recover(id)
		if err != nil || job.State != "unconfirmed" || job.Transaction == nil || !job.Transaction.RequestBound || !strings.Contains(job.Error, "core_config.result") {
			t.Fatal("unsupported query did not preserve Unknown and explain capability failure", err, job)
		}
		logBefore, err := os.ReadFile(calls)
		if err != nil || len(strings.Split(strings.TrimSpace(string(logBefore)), "\n")) != 1 {
			t.Fatal("capability failure sent a query or replayed setter", err)
		}
		t.Setenv("RESPIRE_TEST_CORE_UNSUPPORTED", "0")
		job, err = restarted.Recover(id)
		full.RequestBound = true
		if err != nil || job.State != "done" || job.Transaction == nil || *job.Transaction != full {
			t.Fatal("original-ID query did not discover CLI-bound profile/scope", err, job)
		}
		log, err := os.ReadFile(calls)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(log)), "\n")
		if len(lines) != 2 || strings.Contains(string(log), "--set") {
			t.Fatal("recovery replayed the setter")
		}
		t.Setenv("RESPIRE_TEST_CORE_DROP_REPLY", "0")
		for _, response := range []struct{ raw, state string }{
			{`{"status":"ok","summary":{}}`, "unconfirmed"},
			{`{"status":"fail","summary":{"reason":"invalid_input"}}`, "failed"},
			{`{"status":"fail","details":{"error_type":"outcome_unknown"}}`, "unconfirmed"},
		} {
			if err := os.WriteFile(reply, []byte(response.raw), 0600); err != nil {
				t.Fatal(err)
			}
			bound := hostTransaction{OperationID: strings.TrimPrefix(id, "desktop-"), Kind: "core_config", RequestBound: true}
			m.jobs[id] = &Operation{ID: id, Name: "agent_set", State: "running", host: true, write: true, Transaction: &bound}
			spec, _ := operationSpec("agent_set")
			spec.method = "core_config.set"
			m.runOperation(spec, []string{"agent-config", "--set=recall_fusion={\"fusion\":\"legacy\"}"}, "", id)
			job, err := m.Operation(id)
			if err != nil || job.State != response.state || job.Transaction == nil || *job.Transaction != bound {
				t.Fatal("missing identity was accepted or erased", job.State, err)
			}
		}
	})
	for _, outcome := range []string{"unknown", "applied", "rejected", "foreign_scope"} {
		t.Run("core_config_"+outcome, func(t *testing.T) {
			m, id, reply, calls := recoveryMaterialFixture(t)
			identity := hostTransaction{OperationID: "20000000-0000-4000-8000-000000000002",
				SourceProfile: m.DataRoot, Kind: "core_config", Scope: runtimeScope{"synthetic", "original-instance", 7}}
			m.jobs[id] = &Operation{ID: id, Name: "agent_set", State: "unconfirmed", host: true, write: true, Transaction: &identity}
			if err := m.persistOperationsLocked(); err != nil {
				t.Fatal(err)
			}
			m.jobs = map[string]*Operation{}
			if err := m.loadOperations(); err != nil {
				t.Fatal(err)
			}
			returned := identity
			state, status, errorType := outcome, "ok", ""
			if outcome == "unknown" {
				status, errorType = "fail", "outcome_unknown"
			}
			if outcome == "foreign_scope" {
				returned.Scope.Generation++
				state = "applied"
			}
			if outcome == "rejected" {
				status = "fail"
			}
			details, err := json.Marshal(returned)
			if err != nil {
				t.Fatal(err)
			}
			var metadata map[string]any
			if err := json.Unmarshal(details, &metadata); err != nil {
				t.Fatal(err)
			}
			metadata["error_type"] = errorType
			raw, err := json.Marshal(map[string]any{"status": status, "details": metadata,
				"summary": map[string]any{"core_application": map[string]any{"state": state}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(reply, raw, 0600); err != nil {
				t.Fatal(err)
			}
			job, err := m.Recover(id)
			if err != nil {
				t.Fatal(err)
			}
			want := "done"
			if outcome == "unknown" || outcome == "foreign_scope" {
				want = "unconfirmed"
			}
			if outcome == "rejected" {
				want = "failed"
			}
			if job.State != want || *job.Transaction != identity || job.MaterialSaved {
				t.Fatal("core query changed state or original identity")
			}
			log, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(log)), "\n")
			if len(lines) != 1 {
				t.Fatal("core query replayed a command")
			}
			var args []string
			if err := json.Unmarshal([]byte(lines[0]), &args); err != nil {
				t.Fatal(err)
			}
			if len(args) != 5 || args[2] != "core-config-result" || args[4] != "--source-profile="+identity.SourceProfile {
				t.Fatal("query did not use original result command")
			}
			var context struct {
				Scope       runtimeScope `json:"scope"`
				OperationID string       `json:"operation_id"`
				Key         string       `json:"idempotency_key"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(args[3], "--context=")), &context); err != nil {
				t.Fatal(err)
			}
			if context.Scope != identity.Scope || context.OperationID != identity.OperationID || context.Key != identity.OperationID {
				t.Fatal("query changed original scope or operation")
			}
		})
	}
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
