package webuihandler

import (
	"context"
	"errors"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/values"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/secrets"
)

func testCtx(user *apigen.User) apigen.Context {
	return apigen.Context{Ctx: context.Background(), User: user}
}

func mustCreateDir(t *testing.T, h *Handler, user *apigen.User, spaceID, parentID uint64, name string) *apigen.ValueDirectory {
	t.Helper()
	dir, err := h.PostV1ValueDirectoriesCreate(testCtx(user), &apigen.ValueDirectoryCreateRequest{
		SpaceID: spaceID, ParentID: optID(parentID), Key: name,
	})
	if err != nil {
		t.Fatalf("PostV1ValueDirectoriesCreate(%q): %v", name, err)
	}
	return dir
}

func TestCreateSecretAndConfigInsideDirectory(t *testing.T) {
	h, user := newAuthTestHandler(t)
	dir := mustCreateDir(t, h, user, 1, 0, "postgres")
	if dir.SpaceID != 1 || idOf(dir.ParentID) != 0 {
		t.Fatalf("dir = %+v, want a root directory in space 1 created by %d", dir, user.ID)
	}

	secret, err := h.secretsCreate(testCtx(user), &apigen.SecretCreateRequest{
		Key: "password", Value: []byte("hunter2"), SpaceID: 1, ValueDirectoryID: optID(dir.ID),
	})
	if err != nil {
		t.Fatalf("PostV1SecretsCreate into directory: %v", err)
	}
	if idOf(secret.Value.Fs.DirectoryID) != dir.ID {
		t.Fatalf("secret directory = %d, want %d", idOf(secret.Value.Fs.DirectoryID), dir.ID)
	}

	config, err := h.configsCreate(testCtx(user), &apigen.ConfigCreateRequest{
		Key: "host", Value: "db.internal", SpaceID: 1, ValueDirectoryID: optID(dir.ID),
	})
	if err != nil {
		t.Fatalf("PostV1ConfigsCreate into directory: %v", err)
	}
	if idOf(config.Value.Fs.DirectoryID) != dir.ID {
		t.Fatalf("config directory = %d, want %d", idOf(config.Value.Fs.DirectoryID), dir.ID)
	}

	// The same name is free in the root: the sibling namespace is per directory.
	if _, err := h.secretsCreate(testCtx(user), &apigen.SecretCreateRequest{
		Key: "password", Value: []byte("other"), SpaceID: 1,
	}); err != nil {
		t.Fatalf("PostV1SecretsCreate same name at root: %v", err)
	}
	// But taken inside the directory, across types.
	if _, err := h.configsCreate(testCtx(user), &apigen.ConfigCreateRequest{
		Key: "password", Value: "x", SpaceID: 1, ValueDirectoryID: optID(dir.ID),
	}); !errors.Is(err, UserConfigAlreadyExistsErr) {
		t.Fatalf("config over sibling secret err = %v, want UserConfigAlreadyExistsErr", err)
	}
}

func TestCreateIntoMissingOrForeignDirectoryIsNotFound(t *testing.T) {
	h, user := newAuthTestHandler(t)
	if _, err := h.configsCreate(testCtx(user), &apigen.ConfigCreateRequest{
		Key: "host", Value: "x", SpaceID: 1, ValueDirectoryID: optID(999),
	}); !errors.Is(err, ValueDirectoryNotFoundErr) {
		t.Fatalf("create into missing directory err = %v, want ValueDirectoryNotFoundErr", err)
	}

	space, err := h.PostV1SpacesCreate(testCtx(user), &apigen.SpaceSetRequest{Name: "staging"})
	if err != nil {
		t.Fatalf("PostV1SpacesCreate: %v", err)
	}
	foreign := mustCreateDir(t, h, user, space.ID, 0, "tls")
	// A directory in another space does not exist from this space's viewpoint.
	if _, err := h.secretsCreate(testCtx(user), &apigen.SecretCreateRequest{
		Key: "cert", Value: []byte("pem"), SpaceID: 1, ValueDirectoryID: optID(foreign.ID),
	}); !errors.Is(err, ValueDirectoryNotFoundErr) {
		t.Fatalf("create into foreign-space directory err = %v, want ValueDirectoryNotFoundErr", err)
	}
}

func TestMoveSecretAndConfigBetweenDirectories(t *testing.T) {
	h, user := newAuthTestHandler(t)
	dir := mustCreateDir(t, h, user, 1, 0, "app")

	secret, err := h.secretsCreate(testCtx(user), &apigen.SecretCreateRequest{
		Key: "token", Value: []byte("v"), SpaceID: 1,
	})
	if err != nil {
		t.Fatalf("PostV1SecretsCreate: %v", err)
	}
	moved, err := h.secretsMove(testCtx(user), &apigen.SecretMoveRequest{
		SecretID: secret.SecretID, ValueDirectoryID: optID(dir.ID),
	})
	if err != nil {
		t.Fatalf("PostV1SecretsMove: %v", err)
	}
	if idOf(moved.Value.Fs.DirectoryID) != dir.ID {
		t.Fatalf("moved secret directory = %d, want %d", idOf(moved.Value.Fs.DirectoryID), dir.ID)
	}
	// The version index survives the move untouched.
	if len(statetest.ValueVersions(h.Store, moved)) != 1 || statetest.ValueVersions(h.Store, moved)[0].Ref != statetest.ValueVersions(h.Store, secret)[0].Ref {
		t.Fatalf("versions changed across the move: %+v vs %+v", statetest.ValueVersions(h.Store, moved), statetest.ValueVersions(h.Store, secret))
	}

	config, err := h.configsCreate(testCtx(user), &apigen.ConfigCreateRequest{
		Key: "level", Value: "info", SpaceID: 1,
	})
	if err != nil {
		t.Fatalf("PostV1ConfigsCreate: %v", err)
	}
	movedCfg, err := h.configsMove(testCtx(user), &apigen.ConfigMoveRequest{
		ConfigID: config.ConfigID, ValueDirectoryID: optID(dir.ID),
	})
	if err != nil {
		t.Fatalf("PostV1ConfigsMove: %v", err)
	}
	if idOf(movedCfg.Value.Fs.DirectoryID) != dir.ID {
		t.Fatalf("moved config directory = %d, want %d", idOf(movedCfg.Value.Fs.DirectoryID), dir.ID)
	}

	// A sibling with the same name blocks the move back out.
	if _, err := h.configsCreate(testCtx(user), &apigen.ConfigCreateRequest{
		Key: "level", Value: "root", SpaceID: 1,
	}); err != nil {
		t.Fatalf("PostV1ConfigsCreate at root: %v", err)
	}
	if _, err := h.configsMove(testCtx(user), &apigen.ConfigMoveRequest{
		ConfigID: config.ConfigID, ValueDirectoryID: optID(0),
	}); !errors.Is(err, UserConfigAlreadyExistsErr) {
		t.Fatalf("move onto taken name err = %v, want UserConfigAlreadyExistsErr", err)
	}
}

// Items move across spaces when nothing outside the destination references
// them (an unreferenced item trivially qualifies). Directories still refuse:
// a subtree move needs per-item reference checks and stays unsupported.
func TestCrossSpaceValueMove(t *testing.T) {
	h, user := newAuthTestHandler(t)
	dir := mustCreateDir(t, h, user, 1, 0, "app")
	nested := mustCreateDir(t, h, user, 1, dir.ID, "conf")

	secret, err := h.secretsCreate(testCtx(user), &apigen.SecretCreateRequest{
		Key: "token", Value: []byte("v"), SpaceID: 1, ValueDirectoryID: optID(dir.ID),
	})
	if err != nil {
		t.Fatalf("PostV1SecretsCreate: %v", err)
	}
	config, err := h.configsCreate(testCtx(user), &apigen.ConfigCreateRequest{
		Key: "level", Value: "info", SpaceID: 1, ValueDirectoryID: optID(dir.ID),
	})
	if err != nil {
		t.Fatalf("PostV1ConfigsCreate: %v", err)
	}

	movedSecret, err := h.secretsMove(testCtx(user), &apigen.SecretMoveRequest{
		SecretID: secret.SecretID, ValueDirectoryID: optID(0), SpaceID: optID(2),
	})
	if err != nil {
		t.Fatalf("cross-space secret move: %v", err)
	}
	if movedSecret.SpaceID() != 2 || idOf(movedSecret.Value.Fs.DirectoryID) != 0 {
		t.Fatalf("moved secret = space %d dir %d, want space 2 dir 0", movedSecret.SpaceID(), idOf(movedSecret.Value.Fs.DirectoryID))
	}
	// The version index survives the move untouched: deployment specs pin
	// version row ids.
	if len(statetest.ValueVersions(h.Store, movedSecret)) != 1 || statetest.ValueVersions(h.Store, movedSecret)[0].Ref != statetest.ValueVersions(h.Store, secret)[0].Ref {
		t.Fatalf("versions changed across the move: %+v vs %+v", statetest.ValueVersions(h.Store, movedSecret), statetest.ValueVersions(h.Store, secret))
	}

	movedCfg, err := h.configsMove(testCtx(user), &apigen.ConfigMoveRequest{
		ConfigID: config.ConfigID, ValueDirectoryID: optID(0), SpaceID: optID(2),
	})
	if err != nil {
		t.Fatalf("cross-space config move: %v", err)
	}
	if movedCfg.SpaceID() != 2 || idOf(movedCfg.Value.Fs.DirectoryID) != 0 {
		t.Fatalf("moved config = space %d dir %d, want space 2 dir 0", movedCfg.SpaceID(), idOf(movedCfg.Value.Fs.DirectoryID))
	}

	if _, err := h.PostV1ValueDirectoriesMove(testCtx(user), &apigen.ValueDirectoryMoveRequest{
		DirectoryID: nested.ID, NewParentID: optID(0), SpaceID: optID(2),
	}); !errors.Is(err, ValueSpaceMoveUnsupportedErr) {
		t.Fatalf("cross-space directory move err = %v, want ValueSpaceMoveUnsupportedErr", err)
	}
	if meta, ok := values.DirectoryMeta(h.Store.Queries(), nested.ID); !ok || idOf(meta.ParentID) != dir.ID || meta.SpaceID != 1 {
		t.Fatalf("directory = %+v after a rejected move, want space 1 parent %d", meta, dir.ID)
	}

	// Naming the row's own space is a no-op, not a rejection: the explorer sends
	// the target space on every drop, including same-space ones.
	if _, err := h.secretsMove(testCtx(user), &apigen.SecretMoveRequest{
		SecretID: secret.SecretID, ValueDirectoryID: optID(0), SpaceID: optID(2),
	}); err != nil {
		t.Fatalf("same-space move with an explicit space: %v", err)
	}

	// The moved secret's value is still reachable through its new space —
	// the Manager's denormalized space follows the identity row.
	if revealed, err := h.PostV1SecretsReveal(testCtx(user), &apigen.SecretRevealRequest{
		SecretID: secret.SecretID, Version: statetest.ValueVersions(h.Store, secret)[0].Version,
	}); err != nil || string(revealed.Value) != "v" {
		t.Fatalf("reveal after move = %v, %v", revealed, err)
	}
	if meta, ok := h.Secrets.MetaByRef(statetest.ValueVersions(h.Store, secret)[0].Ref); !ok || meta.SpaceID != 2 {
		t.Fatalf("manager meta after move = %+v ok=%v, want space 2", meta, ok)
	}
}

// A cross-space move is refused while anything outside the destination space
// pins the value: deployments elsewhere, or cluster settings (which pin the
// value to the global space).
func TestCrossSpaceValueMoveBlockedByReferences(t *testing.T) {
	h, user := newAuthTestHandler(t)

	secret, err := h.secretsCreate(testCtx(user), &apigen.SecretCreateRequest{
		Key: "token", Value: []byte("v"), SpaceID: 1,
	})
	if err != nil {
		t.Fatalf("PostV1SecretsCreate: %v", err)
	}
	config, err := h.configsCreate(testCtx(user), &apigen.ConfigCreateRequest{
		Key: "level", Value: "info", SpaceID: 1,
	})
	if err != nil {
		t.Fatalf("PostV1ConfigsCreate: %v", err)
	}
	certSecret, err := h.secretsCreate(testCtx(user), &apigen.SecretCreateRequest{
		Key: "cert", Value: []byte("pem"), SpaceID: 1,
	})
	if err != nil {
		t.Fatalf("PostV1SecretsCreate: %v", err)
	}

	spec := remoteDeploymentSpec("registry/web", virtualNetworking())
	spec.Workload.Value.Container.Runtime.EnvVars = map[string]apigen.EnvVar{
		"TOKEN": secretEnv(statetest.ValueVersions(h.Store, secret)[0].Ref),
		"LEVEL": configEnv(statetest.ValueVersions(h.Store, config)[0].Ref),
	}
	spec.Networking.Ingress = []apigen.Ingress{httpsIngress("web.test", 8080, secretCertSource(statetest.ValueVersions(h.Store, certSecret)[0].Ref))}
	createTestDeployment(h.Store, "node1", 1, "web", &spec)

	// All three pins live in space 1, so nothing may leave it.
	if _, err := h.secretsMove(testCtx(user), &apigen.SecretMoveRequest{
		SecretID: secret.SecretID, SpaceID: optID(2),
	}); !errors.Is(err, deployments.MoveReferencesOutsideSpaceErr) {
		t.Fatalf("referenced secret move err = %v, want deployments.MoveReferencesOutsideSpaceErr", err)
	}
	if _, err := h.configsMove(testCtx(user), &apigen.ConfigMoveRequest{
		ConfigID: config.ConfigID, SpaceID: optID(2),
	}); !errors.Is(err, deployments.MoveReferencesOutsideSpaceErr) {
		t.Fatalf("referenced config move err = %v, want deployments.MoveReferencesOutsideSpaceErr", err)
	}
	// The ingress cert pin counts even though no env var names the secret.
	if _, err := h.secretsMove(testCtx(user), &apigen.SecretMoveRequest{
		SecretID: certSecret.SecretID, SpaceID: optID(2),
	}); !errors.Is(err, deployments.MoveReferencesOutsideSpaceErr) {
		t.Fatalf("cert-referenced secret move err = %v, want deployments.MoveReferencesOutsideSpaceErr", err)
	}
	// It blocks deletion too, exactly like an env pin — and the refusal names
	// the pinning deployment so the UI can say what to detach first.
	if err := h.PostV1SecretsDelete(testCtx(user), &apigen.SecretDeleteRequest{
		SecretID: certSecret.SecretID,
	}); !errors.Is(err, deployments.ReferenceInUseErr) {
		t.Fatalf("cert-referenced secret delete err = %v, want deployments.ReferenceInUseErr", err)
	} else {
		var apiErr apigen.ApiErr
		if !errors.As(err, &apiErr) || !strings.HasSuffix(apiErr.DisplayErr, "/ web") {
			t.Fatalf("cert-referenced secret delete display = %q, want referencing deployment named", apiErr.DisplayErr)
		}
	}

	// A value whose only references live in the destination space may move
	// there: this secret sits in space 2 but is pinned from space 1.
	stray, err := h.secretsCreate(testCtx(user), &apigen.SecretCreateRequest{
		Key: "stray", Value: []byte("s"), SpaceID: 2,
	})
	if err != nil {
		t.Fatalf("PostV1SecretsCreate: %v", err)
	}
	straySpec := remoteDeploymentSpec("registry/secondary", virtualNetworking())
	straySpec.Workload.Value.Container.Runtime.EnvVars = map[string]apigen.EnvVar{
		"STRAY": secretEnv(statetest.ValueVersions(h.Store, stray)[0].Ref),
	}
	createTestDeployment(h.Store, "node1", 1, "secondary", &straySpec)
	moved, err := h.secretsMove(testCtx(user), &apigen.SecretMoveRequest{
		SecretID: stray.SecretID, SpaceID: optID(1),
	})
	if err != nil {
		t.Fatalf("move toward the referencing space: %v", err)
	}
	if moved.SpaceID() != 1 {
		t.Fatalf("moved secret space = %d, want 1", moved.SpaceID())
	}

	// A settings reference pins the value to the global space.
	pinned, err := h.secretsCreate(testCtx(user), &apigen.SecretCreateRequest{
		Key: "gh-token", Value: []byte("t"), SpaceID: 1,
	})
	if err != nil {
		t.Fatalf("PostV1SecretsCreate: %v", err)
	}
	settings := h.SystemConfig.Snapshot().Settings
	settings.Repo.GithubToken = apigen.Some(secretRefOf(statetest.ValueVersions(h.Store, pinned)[0].Ref))
	if err := h.SystemConfig.UpdateSettingsInternal(settings); err != nil {
		t.Fatalf("UpdateSettingsInternal: %v", err)
	}
	if _, err := h.secretsMove(testCtx(user), &apigen.SecretMoveRequest{
		SecretID: pinned.SecretID, SpaceID: optID(2),
	}); !errors.Is(err, deployments.MoveReferencesOutsideSpaceErr) {
		t.Fatalf("settings-referenced secret move err = %v, want deployments.MoveReferencesOutsideSpaceErr", err)
	}
}

func TestMoveReservedSecretIsRejected(t *testing.T) {
	h, user := newAuthTestHandler(t)
	dir := mustCreateDir(t, h, user, 1, 0, "misc")

	// Reserved names cannot be created through the Manager at all, so seed one
	// directly at the storage layer the way install/restore-era rows exist.
	rec, err := secrets.CreateWithVersion(h.Store, "opendeploy.cluster-ca", 1, 0, 0,
		func(uint64) (secrets.SealedValue, error) {
			return secrets.SealedValue{SMKVersion: 1, Ciphertext: []byte{1}, Nonce: []byte{2}}, nil
		})
	if err != nil {
		t.Fatalf("seeding reserved secret: %v", err)
	}
	if _, err := h.secretsMove(testCtx(user), &apigen.SecretMoveRequest{
		SecretID: rec.SecretID, ValueDirectoryID: optID(dir.ID),
	}); !errors.Is(err, SecretReservedNameErr) {
		t.Fatalf("moving reserved secret err = %v, want SecretReservedNameErr", err)
	}
}

func TestRenameAndMoveDirectories(t *testing.T) {
	h, user := newAuthTestHandler(t)
	parent := mustCreateDir(t, h, user, 1, 0, "a")
	child := mustCreateDir(t, h, user, 1, parent.ID, "b")

	renamed, err := h.PostV1ValueDirectoriesRename(testCtx(user), &apigen.ValueDirectoryRenameRequest{
		DirectoryID: child.ID, NewKey: "c",
	})
	if err != nil {
		t.Fatalf("PostV1ValueDirectoriesRename: %v", err)
	}
	if renamed.Key != "c" || idOf(renamed.ParentID) != parent.ID {
		t.Fatalf("renamed = %+v, want name c under parent %d", renamed, parent.ID)
	}

	// The rename target namespace spans secrets, configs, and directories.
	if _, err := h.configsCreate(testCtx(user), &apigen.ConfigCreateRequest{
		Key: "taken", Value: "x", SpaceID: 1, ValueDirectoryID: optID(parent.ID),
	}); err != nil {
		t.Fatalf("PostV1ConfigsCreate: %v", err)
	}
	if _, err := h.PostV1ValueDirectoriesRename(testCtx(user), &apigen.ValueDirectoryRenameRequest{
		DirectoryID: child.ID, NewKey: "taken",
	}); !errors.Is(err, ValueDirectoryNameTakenErr) {
		t.Fatalf("rename onto sibling config err = %v, want ValueDirectoryNameTakenErr", err)
	}

	// A directory cannot be moved inside its own subtree.
	if _, err := h.PostV1ValueDirectoriesMove(testCtx(user), &apigen.ValueDirectoryMoveRequest{
		DirectoryID: parent.ID, NewParentID: optID(child.ID),
	}); !errors.Is(err, ValueDirectoryCycleErr) {
		t.Fatalf("cycle move err = %v, want ValueDirectoryCycleErr", err)
	}

	moved, err := h.PostV1ValueDirectoriesMove(testCtx(user), &apigen.ValueDirectoryMoveRequest{
		DirectoryID: child.ID, NewParentID: optID(0),
	})
	if err != nil {
		t.Fatalf("PostV1ValueDirectoriesMove to root: %v", err)
	}
	if idOf(moved.ParentID) != 0 {
		t.Fatalf("moved parent = %d, want the root", idOf(moved.ParentID))
	}
}

func TestDeleteDirectoryOnlyWhenEmpty(t *testing.T) {
	h, user := newAuthTestHandler(t)
	dir := mustCreateDir(t, h, user, 1, 0, "tmp")
	config, err := h.configsCreate(testCtx(user), &apigen.ConfigCreateRequest{
		Key: "k", Value: "v", SpaceID: 1, ValueDirectoryID: optID(dir.ID),
	})
	if err != nil {
		t.Fatalf("PostV1ConfigsCreate: %v", err)
	}

	if err := h.PostV1ValueDirectoriesDelete(testCtx(user), &apigen.ValueDirectoryDeleteRequest{
		DirectoryID: dir.ID,
	}); !errors.Is(err, ValueDirectoryNotEmptyErr) {
		t.Fatalf("delete of non-empty directory err = %v, want ValueDirectoryNotEmptyErr", err)
	}

	if _, err := h.configsMove(testCtx(user), &apigen.ConfigMoveRequest{ConfigID: config.ConfigID}); err != nil {
		t.Fatalf("PostV1ConfigsMove to root: %v", err)
	}
	if err := h.PostV1ValueDirectoriesDelete(testCtx(user), &apigen.ValueDirectoryDeleteRequest{
		DirectoryID: dir.ID,
	}); err != nil {
		t.Fatalf("delete of emptied directory: %v", err)
	}

	list, err := h.PostV1ValueDirectoriesList(testCtx(user))
	if err != nil {
		t.Fatalf("PostV1ValueDirectoriesList: %v", err)
	}
	for _, d := range list.Items {
		if d.ID == dir.ID {
			t.Fatalf("deleted directory still listed: %+v", d)
		}
	}
}

func TestGlobalStateIncludesValueDirectories(t *testing.T) {
	h, user := newAuthTestHandler(t)
	dir := mustCreateDir(t, h, user, 1, 0, "app")

	msg, err := h.PostV1GlobalEvents(testCtx(user), &apigen.EventStreamRequest{})
	if err != nil {
		t.Fatalf("PostV1GlobalEvents: %v", err)
	}
	dirs := foldOpening(msg)[apigen.CoreEntityType_CORE_ENTITY_VALUE_DIRECTORY]
	if len(dirs) != 1 || dirs[dir.ID] == nil || dirs[dir.ID].Value.ValueDirectory.ID != dir.ID {
		t.Fatalf("bootstrap value directories = %+v, want the one created", dirs)
	}
}
