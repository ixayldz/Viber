//go:build !linux && !darwin

package agent

// Windows runs the same actual 512-file retirement; Unix additionally measures
// the child's OS-enforced 64-descriptor ceiling. No Windows ceiling is claimed.
func operationTestDescriptorLimit() error { return nil }
