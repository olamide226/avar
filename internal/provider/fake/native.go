package fake

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/olamide226/avar/internal/types"
)

// The Fake's model of Linux-native workspace mode.
//
// It keeps two manifests per project and a baseline, and it applies a
// synchronization to them for real: after ApplyNativeWorkspace the copied files
// are in the destination manifest and the deleted ones are gone. A double that
// only recorded the call could not catch the mistake that matters most here — a
// flow that asks for the wrong direction, or that applies a plan it should have
// refused — because the recording would look identical either way.
//
// The workspace path is deliberately not the mount path, for the same reason
// the Fake's guest project root is not the host path: a caller that confuses the
// two copies must fail in a unit test rather than on a user's machine.

// WorkspacesRoot is where the Fake places a project's native copy.
const WorkspacesRoot = "/home/fake/workspaces"

// nativeWorkspace is the Fake's model of one project's two copies.
type nativeWorkspace struct {
	// guest is the native copy, nil when it has never been created.
	guest types.WorkspaceManifest
	// mount is what the shared host copy holds.
	mount types.WorkspaceManifest
	// baseline is what the last completed synchronization recorded.
	baseline types.WorkspaceManifest
	// skipped is programmed, for the flows that have to report entries avar
	// will not carry.
	skipped []string
}

// SetWorkspaceHost programs what the shared host copy of a project holds.
func (f *Fake) SetWorkspaceHost(workspacePath string, m types.WorkspaceManifest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.workspace(workspacePath).mount = m.Clone()
}

// SetWorkspaceGuest programs what the native copy holds, and thereby that it
// exists at all. A nil manifest means the workspace has never been created.
func (f *Fake) SetWorkspaceGuest(workspacePath string, m types.WorkspaceManifest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ws := f.workspace(workspacePath)
	if m == nil {
		ws.guest = nil
		return
	}
	ws.guest = m.Clone()
}

// SetWorkspaceBaseline programs what the last completed synchronization
// recorded that both copies agreed on.
func (f *Fake) SetWorkspaceBaseline(workspacePath string, m types.WorkspaceManifest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m == nil {
		f.workspace(workspacePath).baseline = nil
		return
	}
	f.workspace(workspacePath).baseline = m.Clone()
}

// SetWorkspaceSkipped programs the entries avar will not carry.
func (f *Fake) SetWorkspaceSkipped(workspacePath string, skipped []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.workspace(workspacePath).skipped = append([]string(nil), skipped...)
}

// WorkspaceGuest reports what the native copy holds now, so a test can prove a
// synchronization landed rather than only that it was requested.
func (f *Fake) WorkspaceGuest(workspacePath string) types.WorkspaceManifest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.workspace(workspacePath).guest.Clone()
}

// WorkspaceHost reports what the shared host copy holds now.
func (f *Fake) WorkspaceHost(workspacePath string) types.WorkspaceManifest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.workspace(workspacePath).mount.Clone()
}

// WorkspaceBaseline reports the recorded agreement.
func (f *Fake) WorkspaceBaseline(workspacePath string) types.WorkspaceManifest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.workspace(workspacePath).baseline.Clone()
}

// workspace returns the model for a path, creating an empty one on first use.
// The caller holds the lock.
func (f *Fake) workspace(workspacePath string) *nativeWorkspace {
	if f.workspaces == nil {
		f.workspaces = make(map[string]*nativeWorkspace)
	}
	ws, ok := f.workspaces[workspacePath]
	if !ok {
		ws = &nativeWorkspace{}
		f.workspaces[workspacePath] = ws
	}
	return ws
}

// MapNativeWorkspace plans a native copy beneath the Fake's own workspaces root.
//
// It is not recorded as a call, for the reason MapProjectPath is not: the
// contract makes it a pure function, and what a flow has to get right is that
// the directory it eventually hands to Shell is the one the provider planned.
func (f *Fake) MapNativeWorkspace(projectID, hostRoot, hostCwd string) (types.NativeWorkspace, string, error) {
	mount, mountCwd, err := f.MapProjectPath(projectID, hostRoot, hostCwd)
	if err != nil {
		return types.NativeWorkspace{}, "", err
	}

	root := path.Join(WorkspacesRoot, projectID)
	ws := types.NativeWorkspace{
		ProjectID: projectID,
		HostPath:  mount.HostPath,
		MountPath: mount.GuestPath,
		Path:      root,
	}
	if err := ws.Validate(); err != nil {
		return types.NativeWorkspace{}, "", fmt.Errorf("planning a Linux-native workspace for %s: %w", filepath.Clean(hostRoot), err)
	}

	guestCwd := root
	if rel := strings.TrimPrefix(mountCwd, mount.GuestPath); rel != "" {
		guestCwd = path.Join(root, strings.TrimPrefix(rel, "/"))
	}
	return ws, guestCwd, nil
}

// ScanNativeWorkspace reports what each copy holds.
func (f *Fake) ScanNativeWorkspace(ctx context.Context, name string, ws types.NativeWorkspace) (types.WorkspaceScan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	call := Call{Op: OpScanNativeWorkspace, Machine: name, Workspace: ws}
	var scan types.WorkspaceScan
	if err := f.gate(ctx, OpScanNativeWorkspace, name); err != nil {
		call.Err = err
	} else if err := ws.Validate(); err != nil {
		call.Err = err
	} else if _, err := f.running(name); err != nil {
		call.Err = err
	} else {
		model := f.workspace(ws.Path)
		scan = types.WorkspaceScan{
			Exists:   model.guest != nil,
			Baseline: model.baseline.Clone(),
			Mount:    model.mount.Clone(),
			Guest:    model.guest.Clone(),
			Skipped:  append([]string(nil), model.skipped...),
		}
		if model.baseline == nil {
			scan.Baseline = nil
		}
	}
	f.calls = append(f.calls, call)
	return scan, call.Err
}

// ApplyNativeWorkspace copies and deletes in the requested direction, then
// records the baseline the synchronization left behind.
func (f *Fake) ApplyNativeWorkspace(ctx context.Context, name string, ws types.NativeWorkspace, sync types.WorkspaceSync, progress types.ProgressSink) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	call := Call{Op: OpApplyNativeWorkspace, Machine: name, Workspace: ws, Sync: cloneSync(sync)}
	call.Err = f.applyNativeWorkspace(ctx, name, ws, sync, progress)
	f.calls = append(f.calls, call)
	return call.Err
}

func (f *Fake) applyNativeWorkspace(ctx context.Context, name string, ws types.NativeWorkspace, sync types.WorkspaceSync, progress types.ProgressSink) error {
	if err := f.gate(ctx, OpApplyNativeWorkspace, name); err != nil {
		return err
	}
	if err := ws.Validate(); err != nil {
		return err
	}
	if _, err := f.running(name); err != nil {
		return err
	}
	if sync.Direction != types.ToGuest && sync.Direction != types.ToHost {
		return fmt.Errorf("%q is not a direction avar knows", sync.Direction)
	}

	model := f.workspace(ws.Path)
	if model.guest == nil {
		model.guest = types.WorkspaceManifest{}
	}
	if model.mount == nil {
		model.mount = types.WorkspaceManifest{}
	}

	src, dst := model.mount, model.guest
	if sync.Direction == types.ToHost {
		src, dst = model.guest, model.mount
	}

	for _, rel := range sync.Copy {
		entry, ok := src[rel]
		if !ok {
			return fmt.Errorf("synchronizing %s: %s is not in the copy it would come from", ws.HostPath, rel)
		}
		dst[rel] = entry
	}
	for _, rel := range sync.Delete {
		delete(dst, rel)
	}

	// The baseline last, exactly as a backend must write it: a test that
	// interrupts an apply has to see the previous agreement still recorded.
	model.baseline = sync.Baseline.Clone()

	if !sync.Empty() {
		f.emit(progress, types.ProgressEvent{
			Kind:    types.ProgressSyncing,
			Machine: name,
			Message: fmt.Sprintf("copying %d files %s", len(sync.Copy), sync.Direction),
		})
	}
	return nil
}

// cloneSync copies a synchronization so a recorded call cannot be mutated by
// whatever the caller does with its argument afterwards.
func cloneSync(s types.WorkspaceSync) types.WorkspaceSync {
	s.Copy = append([]string(nil), s.Copy...)
	s.Delete = append([]string(nil), s.Delete...)
	s.Baseline = s.Baseline.Clone()
	return s
}
