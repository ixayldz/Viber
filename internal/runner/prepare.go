package runner

import (
	"path/filepath"
)

func PrepareTarget(target Target) (Target, Capsule, error) {
	source, err := filepath.Abs(target.Source)
	if err != nil {
		return target, Capsule{}, err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return target, Capsule{}, err
	}
	target.Source = source
	capsule, err := makeCapsule(target.Owner, source, target.Profile, target.Invocation)
	return target, capsule, err
}
