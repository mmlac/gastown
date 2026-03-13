package dispatch

import (
	"fmt"
	"testing"
)

func rigClassifier(rigs ...string) func(string) (string, bool) {
	set := make(map[string]bool, len(rigs))
	for _, r := range rigs {
		set[r] = true
	}
	return func(target string) (string, bool) {
		if set[target] {
			return target, true
		}
		return "", false
	}
}

func dogClassifier(dogs map[string]string) func(string) (string, bool) {
	return func(target string) (string, bool) {
		name, ok := dogs[target]
		return name, ok
	}
}

func rigFactory(t *testing.T) func(string) (DispatchTarget, error) {
	return func(rigName string) (DispatchTarget, error) {
		spawn := testSpawnResult()
		spawn.RigName = rigName
		return NewRigTarget(
			rigName,
			successSpawn(spawn),
			successStart("%1"),
			noopRollback(),
			sessionRunning(false),
		), nil
	}
}

func dogFactory() func(string) (DispatchTarget, error) {
	return func(dogName string) (DispatchTarget, error) {
		return NewDogTarget(DogTargetConfig{DogName: dogName}), nil
	}
}

func TestResolveTarget_EmptyTarget(t *testing.T) {
	_, err := ResolveTarget("", TargetClassifiers{}, TargetFactories{})
	if err == nil {
		t.Error("expected error for empty target")
	}
}

func TestResolveTarget_RigTarget(t *testing.T) {
	classifiers := TargetClassifiers{
		IsRigName: rigClassifier("gastown", "greenplace"),
	}
	factories := TargetFactories{
		NewRigTarget: rigFactory(t),
	}

	target, err := ResolveTarget("gastown", classifiers, factories)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.TargetType() != "rig" {
		t.Errorf("TargetType() = %q, want %q", target.TargetType(), "rig")
	}
}

func TestResolveTarget_DogTarget(t *testing.T) {
	classifiers := TargetClassifiers{
		IsRigName:   rigClassifier(), // no rigs
		IsDogTarget: dogClassifier(map[string]string{"deacon/dogs/alpha": "alpha", "deacon/dogs": ""}),
	}
	factories := TargetFactories{
		NewDogTarget: dogFactory(),
	}

	target, err := ResolveTarget("deacon/dogs/alpha", classifiers, factories)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.TargetType() != "dog" {
		t.Errorf("TargetType() = %q, want %q", target.TargetType(), "dog")
	}
}

func TestResolveTarget_DogPoolTarget(t *testing.T) {
	classifiers := TargetClassifiers{
		IsDogTarget: dogClassifier(map[string]string{"deacon/dogs": ""}),
	}
	factories := TargetFactories{
		NewDogTarget: dogFactory(),
	}

	target, err := ResolveTarget("deacon/dogs", classifiers, factories)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.TargetType() != "dog" {
		t.Errorf("TargetType() = %q, want %q", target.TargetType(), "dog")
	}
}

func TestResolveTarget_ExistingAgentFallback(t *testing.T) {
	classifiers := TargetClassifiers{
		IsRigName:   rigClassifier(),
		IsDogTarget: dogClassifier(map[string]string{}),
	}
	factories := TargetFactories{}

	target, err := ResolveTarget("mayor", classifiers, factories)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.TargetType() != "agent" {
		t.Errorf("TargetType() = %q, want %q", target.TargetType(), "agent")
	}
	if target.AgentID() != "mayor" {
		t.Errorf("AgentID() = %q, want %q", target.AgentID(), "mayor")
	}
}

func TestResolveTarget_NoClassifiers(t *testing.T) {
	// With no classifiers, everything falls through to ExistingAgentTarget.
	target, err := ResolveTarget("gastown", TargetClassifiers{}, TargetFactories{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.TargetType() != "agent" {
		t.Errorf("TargetType() = %q, want %q", target.TargetType(), "agent")
	}
}

func TestResolveTarget_RigMatchedButNoFactory(t *testing.T) {
	classifiers := TargetClassifiers{
		IsRigName: rigClassifier("gastown"),
	}
	factories := TargetFactories{} // no rig factory

	_, err := ResolveTarget("gastown", classifiers, factories)
	if err == nil {
		t.Error("expected error when rig matched but no factory")
	}
}

func TestResolveTarget_DogMatchedButNoFactory(t *testing.T) {
	classifiers := TargetClassifiers{
		IsDogTarget: dogClassifier(map[string]string{"deacon/dogs/alpha": "alpha"}),
	}
	factories := TargetFactories{} // no dog factory

	_, err := ResolveTarget("deacon/dogs/alpha", classifiers, factories)
	if err == nil {
		t.Error("expected error when dog matched but no factory")
	}
}

func TestResolveTarget_RigFactoryError(t *testing.T) {
	classifiers := TargetClassifiers{
		IsRigName: rigClassifier("gastown"),
	}
	factories := TargetFactories{
		NewRigTarget: func(rigName string) (DispatchTarget, error) {
			return nil, fmt.Errorf("spawn failed")
		},
	}

	_, err := ResolveTarget("gastown", classifiers, factories)
	if err == nil {
		t.Error("expected error when rig factory fails")
	}
}

func TestResolveTarget_PriorityRigOverDog(t *testing.T) {
	// If something matches both rig and dog (unlikely but test ordering),
	// rig should win since it's checked first.
	classifiers := TargetClassifiers{
		IsRigName:   rigClassifier("ambiguous"),
		IsDogTarget: dogClassifier(map[string]string{"ambiguous": "ambiguous"}),
	}
	factories := TargetFactories{
		NewRigTarget: rigFactory(t),
		NewDogTarget: dogFactory(),
	}

	target, err := ResolveTarget("ambiguous", classifiers, factories)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.TargetType() != "rig" {
		t.Errorf("TargetType() = %q, want rig (rig should take priority)", target.TargetType())
	}
}
