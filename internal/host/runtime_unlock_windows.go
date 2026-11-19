package host

import "context"

func (m *Manager) unlockAccountRuntime(ctx context.Context, health HealthInfo) error {
	return m.unlockNativeRuntime(ctx, health, nativeRuntimeCredentialStore(ctx))
}
