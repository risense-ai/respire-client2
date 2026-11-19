//go:build linux || (darwin && !cgo)

package host

import "context"

func (m *Manager) unlockAccountRuntime(ctx context.Context, _ HealthInfo) error {
	_, err := m.hostOperation(ctx, []string{"runtime", "unlock"}, "", "runtime.unlock", "")
	return err
}
