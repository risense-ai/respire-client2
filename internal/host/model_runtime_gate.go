package host

import (
	"context"
	"fmt"

	"golang.org/x/mod/semver"
)

// Minimum runtime versions for the model-selection features the wizard and
// models page rely on, verified with `git tag --contains` in respire-cli:
// v1.0.20-dev.5 added `model engine --model` / `install-m3 --model`
// (selectable onnx variants), v1.0.20-dev.10 added the model.configure and
// model.switch profile RPCs.
const (
	minVariantRuntimeVersion = "1.0.20-dev.5"
	minProfileRuntimeVersion = "1.0.20-dev.10"
)

// runtimeAtLeast compares the runtime's /api/health "bin" version against a
// minimum; an unknown or malformed version never passes.
func runtimeAtLeast(bin, min string) bool {
	return semver.IsValid("v"+bin) && semver.Compare("v"+bin, "v"+min) >= 0
}

// ensureRuntimeFeature fails the operation with an actionable message when
// the running runtime predates a model-selection feature, instead of
// relaying the CLI's raw clap error to the user.
func (m *Manager) ensureRuntimeFeature(ctx context.Context, minVersion, feature string) error {
	health, _, err := m.Health(ctx)
	if err != nil {
		return err
	}
	if runtimeAtLeast(health.Version, minVersion) {
		return nil
	}
	if semver.IsValid("v" + health.Version) {
		return fmt.Errorf("当前 Runtime 版本 %s 过旧，不支持%s（需要 ≥ %s）；请更新 Runtime 并重启应用后重试", health.Version, feature, minVersion)
	}
	return fmt.Errorf("无法确认当前 Runtime 版本，不支持%s（需要 ≥ %s）；请更新 Runtime 并重启应用后重试", feature, minVersion)
}
