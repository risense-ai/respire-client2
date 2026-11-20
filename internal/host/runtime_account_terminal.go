package host

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const runtimeMaintenanceLimit = 16 * 1024

func controlDigest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

type runtimeAccountRoute struct {
	Schema      int    `json:"schema"`
	OperationID string `json:"operation_id"`
}

type runtimeTerminalMaintenance struct {
	Schema       int    `json:"schema"`
	MarkerSHA256 string `json:"marker_sha256"`
	RouteSHA256  string `json:"route_sha256"`
	RecordSHA256 string `json:"record_sha256"`
}

// The caller holds takeover from this check through startup or account retirement.
func archiveTerminalRuntimeMaintenance(root string) error {
	canonical, err := canonicalSessionDirectory(root)
	if err != nil {
		return err
	}
	if !samePath(canonical, root) {
		return errors.New("Runtime 安装根目录已重定向")
	}
	directory := filepath.Join(canonical, "runtime", "autostart")
	if err := checkRuntimeAccountDirectory(directory); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	markerPath := filepath.Join(directory, "maintenance.json")
	marker, err := readCredentialConfig(markerPath, runtimeMaintenanceLimit)
	markerAbsent := errors.Is(err, os.ErrNotExist)
	if err != nil && !markerAbsent {
		return err
	}
	routePath := filepath.Join(directory, "account-active.json")
	active, err := readCredentialConfig(routePath, runtimeMaintenanceLimit)
	if markerAbsent && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	blocked := errors.New("Runtime 维护或账户路由缺少可核验的 Client2 终态凭证，请通过原宿主恢复")
	if err != nil {
		return errors.Join(blocked, err)
	}
	var route runtimeAccountRoute
	if json.Unmarshal(active, &route) != nil || route.Schema != 1 || !hostOperationID.MatchString(route.OperationID) {
		return blocked
	}
	custodyPath := filepath.Join(directory, "account-login-"+route.OperationID+".json")
	data, err := readCredentialConfig(custodyPath, runtimeMaintenanceLimit)
	if err != nil {
		return errors.Join(blocked, err)
	}
	var custody struct {
		OperationID  string                     `json:"operation_id"`
		Root         string                     `json:"root"`
		RolledBack   *bool                      `json:"rolled_back"`
		SlotsRemoved bool                       `json:"slots_removed"`
		Retired      bool                       `json:"retired"`
		Terminal     runtimeTerminalMaintenance `json:"client2_terminal_maintenance"`
	}
	if json.Unmarshal(data, &custody) != nil || custody.OperationID != route.OperationID || custody.Root != canonical ||
		custody.RolledBack == nil || *custody.RolledBack || !custody.SlotsRemoved || !custody.Retired ||
		custody.Terminal.Schema != 1 || controlDigest(active) != custody.Terminal.RouteSHA256 {
		return blocked
	}
	results := filepath.Join(directory, "account-results")
	if err := checkRuntimeAccountDirectory(results); err != nil {
		return errors.Join(blocked, err)
	}
	record, err := readCredentialConfig(filepath.Join(results, route.OperationID+".json"), runtimeMaintenanceLimit)
	if err != nil || controlDigest(record) != custody.Terminal.RecordSHA256 {
		return errors.Join(blocked, err)
	}
	markerArchive := filepath.Join(directory, "maintenance.retired-"+route.OperationID+".json")
	routeArchive := filepath.Join(directory, "account-active.retired-"+route.OperationID+".json")
	if err := requireAbsentRuntimeAccountFile(routeArchive); err != nil {
		return err
	}
	if markerAbsent {
		// Only our durable marker move can authorize the route-only crash window.
		marker, err = readCredentialConfig(markerArchive, runtimeMaintenanceLimit)
		if err != nil {
			return errors.Join(blocked, err)
		}
	}
	if controlDigest(marker) != custody.Terminal.MarkerSHA256 {
		return blocked
	}
	for _, name := range []string{"key-control.json", "offline-keygen.json"} {
		if err := requireAbsentRuntimeAccountFile(filepath.Join(directory, name)); err != nil {
			return err
		}
	}
	if !markerAbsent {
		if err := requireAbsentRuntimeAccountFile(markerArchive); err != nil {
			return err
		}
		if err := publishSessionFile(markerPath, markerArchive); err != nil {
			return err
		}
	}
	return publishSessionFile(routePath, routeArchive)
}

func checkRuntimeAccountDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("Runtime 账户维护目录无效")
	}
	actual, err := canonicalSessionDirectory(path)
	if err != nil {
		return err
	}
	if !samePath(path, actual) {
		return errors.New("Runtime 账户维护目录已重定向")
	}
	return nil
}

func requireAbsentRuntimeAccountFile(path string) error {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("Runtime 账户维护文件已存在 (%s)，请通过原宿主恢复", filepath.Base(path))
}
