package dispatch

import "fmt"

// TargetClassifiers provides the detection functions needed by ResolveTarget.
// These are injected by the caller (typically the cmd package) to avoid
// circular imports between dispatch and cmd.
type TargetClassifiers struct {
	// IsRigName returns the rig name and true if the target is a rig.
	IsRigName func(target string) (rigName string, ok bool)

	// IsDogTarget returns the dog name (or empty for pool dispatch) and true
	// if the target is a dog target.
	IsDogTarget func(target string) (dogName string, ok bool)
}

// TargetFactories provides constructor functions for creating DispatchTarget
// implementations. These are injected by the caller because the concrete
// target types require dependencies (spawn functions, config, etc.) that
// live outside the dispatch package.
type TargetFactories struct {
	// NewRigTarget creates a RigTarget for the given rig name.
	// The factory is responsible for wiring spawn/start/rollback/check functions.
	NewRigTarget func(rigName string) (DispatchTarget, error)

	// NewDogTarget creates a DogTarget for the given dog name.
	// Empty dogName indicates pool dispatch (find an idle dog).
	NewDogTarget func(dogName string) (DispatchTarget, error)
}

// ResolveTarget creates the appropriate DispatchTarget from a target string.
//
// It uses the classifiers to determine the target type (rig, dog, or existing
// agent), then delegates to the corresponding factory to construct the target.
// If no classifier matches, an ExistingAgentTarget is returned.
//
// This implements the Resolver pattern from the dispatch design:
//
//	target := ResolveTarget(targetStr, classifiers, factories)
//	target.Prepare(ctx)
//	defer target.Rollback(ctx)
//	target.StartSession(ctx, opts)
func ResolveTarget(target string, classifiers TargetClassifiers, factories TargetFactories) (DispatchTarget, error) {
	if target == "" {
		return nil, fmt.Errorf("empty target string")
	}

	// Check rig target first.
	if classifiers.IsRigName != nil {
		if rigName, ok := classifiers.IsRigName(target); ok {
			if factories.NewRigTarget == nil {
				return nil, fmt.Errorf("rig target %q matched but no rig factory configured", rigName)
			}
			return factories.NewRigTarget(rigName)
		}
	}

	// Check dog target.
	if classifiers.IsDogTarget != nil {
		if dogName, ok := classifiers.IsDogTarget(target); ok {
			if factories.NewDogTarget == nil {
				return nil, fmt.Errorf("dog target %q matched but no dog factory configured", dogName)
			}
			return factories.NewDogTarget(dogName)
		}
	}

	// Fallback: existing agent.
	return NewExistingAgentTarget(target), nil
}
