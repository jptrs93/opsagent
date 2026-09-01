package webuihandler

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
)

func testCtx(user *apigen.InternalUser) apigen.Context {
	return apigen.Context{Ctx: context.Background(), User: user}
}

func mustCreateDir(t *testing.T, h *Handler, user *apigen.InternalUser, spaceID, parentID int32, name string) *apigen.ValueDirectory {
	t.Helper()
	dir, err := h.PostV1ValueDirectoriesCreate(testCtx(user), &apigen.ValueDirectoryCreateRequest{
		SpaceID: spaceID, ParentID: parentID, Name: name,
	})
	if err != nil {
		t.Fatalf("PostV1ValueDirectoriesCreate(%q): %v", name, err)
	}
	return dir
}

func TestCreateSecretAndConfigInsideDirectory(t *testing.T) {
	h, user := newAuthTestHandler(t)
	dir := mustCreateDir(t, h, user, 1, 0, "postgres")
	if dir.SpaceID != 1 || dir.ParentID != 0 || dir.Author != user.ID {
		t.Fatalf("dir = %+v, want a root directory in space 1 created by %d", dir, user.ID)
	}

	secret, err := h.PostV1SecretsCreate(testCtx(user), &apigen.SecretCreateRequest{
		Name: "password", Value: []byte("hunter2"), SpaceID: 1, ValueDirectoryID: dir.ID,
	})
	if err != nil {
		t.Fatalf("PostV1SecretsCreate into directory: %v", err)
	}
	if secret.Fs.DirectoryID != dir.ID {
		t.Fatalf("secret directory = %d, want %d", secret.Fs.DirectoryID, dir.ID)
	}

	config, err := h.PostV1ConfigsCreate(testCtx(user), &apigen.ConfigCreateRequest{
		Name: "host", Value: "db.internal", SpaceID: 1, ValueDirectoryID: dir.ID,
	})
	if err != nil {
		t.Fatalf("PostV1ConfigsCreate into directory: %v", err)
	}
	if config.Fs.DirectoryID != dir.ID {
		t.Fatalf("config directory = %d, want %d", config.Fs.DirectoryID, dir.ID)
	}

	// The same name is free in the root: the sibling namespace is per directory.
	if _, err := h.PostV1SecretsCreate(testCtx(user), &apigen.SecretCreateRequest{
		Name: "password", Value: []byte("other"), SpaceID: 1,
	}); err != nil {
		t.Fatalf("PostV1SecretsCreate same name at root: %v", err)
	}
	// But taken inside the directory, across types.
	if _, err := h.PostV1ConfigsCreate(testCtx(user), &apigen.ConfigCreateRequest{
		Name: "password", Value: "x", SpaceID: 1, ValueDirectoryID: dir.ID,
	}); !errors.Is(err, UserConfigAlreadyExistsErr) {
		t.Fatalf("config over sibling secret err = %v, want UserConfigAlreadyExistsErr", err)
	}
}

func TestCreateIntoMissingOrForeignDirectoryIsNotFound(t *testing.T) {
	h, user := newAuthTestHandler(t)
	if _, err := h.PostV1ConfigsCreate(testCtx(user), &apigen.ConfigCreateRequest{
		Name: "host", Value: "x", SpaceID: 1, ValueDirectoryID: 999,
	}); !errors.Is(err, ValueDirectoryNotFoundErr) {
		t.Fatalf("create into missing directory err = %v, want ValueDirectoryNotFoundErr", err)
	}

	space, err := h.PostV1SpacesCreate(testCtx(user), &apigen.SpaceSetRequest{Name: "staging"})
	if err != nil {
		t.Fatalf("PostV1SpacesCreate: %v", err)
	}
	foreign := mustCreateDir(t, h, user, space.ID, 0, "tls")
	// A directory in another space does not exist from this space's viewpoint.
	if _, err := h.PostV1SecretsCreate(testCtx(user), &apigen.SecretCreateRequest{
		Name: "cert", Value: []byte("pem"), SpaceID: 1, ValueDirectoryID: foreign.ID,
	}); !errors.Is(err, ValueDirectoryNotFoundErr) {
		t.Fatalf("create into foreign-space directory err = %v, want ValueDirectoryNotFoundErr", err)
	}
}

func TestMoveSecretAndConfigBetweenDirectories(t *testing.T) {
	h, user := newAuthTestHandler(t)
	dir := mustCreateDir(t, h, user, 1, 0, "app")

	secret, err := h.PostV1SecretsCreate(testCtx(user), &apigen.SecretCreateRequest{
		Name: "token", Value: []byte("v"), SpaceID: 1,
	})
	if err != nil {
		t.Fatalf("PostV1SecretsCreate: %v", err)
	}
	moved, err := h.PostV1SecretsMove(testCtx(user), &apigen.SecretMoveRequest{
		SecretID: secret.ID, ValueDirectoryID: dir.ID,
	})
	if err != nil {
		t.Fatalf("PostV1SecretsMove: %v", err)
	}
	if moved.Fs.DirectoryID != dir.ID {
		t.Fatalf("moved secret directory = %d, want %d", moved.Fs.DirectoryID, dir.ID)
	}
	// The version index survives the move untouched.
	if len(moved.Versions) != 1 || moved.Versions[0].ID != secret.Versions[0].ID {
		t.Fatalf("versions changed across the move: %+v vs %+v", moved.Versions, secret.Versions)
	}

	config, err := h.PostV1ConfigsCreate(testCtx(user), &apigen.ConfigCreateRequest{
		Name: "level", Value: "info", SpaceID: 1,
	})
	if err != nil {
		t.Fatalf("PostV1ConfigsCreate: %v", err)
	}
	movedCfg, err := h.PostV1ConfigsMove(testCtx(user), &apigen.ConfigMoveRequest{
		ConfigID: config.ID, ValueDirectoryID: dir.ID,
	})
	if err != nil {
		t.Fatalf("PostV1ConfigsMove: %v", err)
	}
	if movedCfg.Fs.DirectoryID != dir.ID {
		t.Fatalf("moved config directory = %d, want %d", movedCfg.Fs.DirectoryID, dir.ID)
	}

	// A sibling with the same name blocks the move back out.
	if _, err := h.PostV1ConfigsCreate(testCtx(user), &apigen.ConfigCreateRequest{
		Name: "level", Value: "root", SpaceID: 1,
	}); err != nil {
		t.Fatalf("PostV1ConfigsCreate at root: %v", err)
	}
	if _, err := h.PostV1ConfigsMove(testCtx(user), &apigen.ConfigMoveRequest{
		ConfigID: config.ID, ValueDirectoryID: 0,
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

	secret, err := h.PostV1SecretsCreate(testCtx(user), &apigen.SecretCreateRequest{
		Name: "token", Value: []byte("v"), SpaceID: 1, ValueDirectoryID: dir.ID,
	})
	if err != nil {
		t.Fatalf("PostV1SecretsCreate: %v", err)
	}
	config, err := h.PostV1ConfigsCreate(testCtx(user), &apigen.ConfigCreateRequest{
		Name: "level", Value: "info", SpaceID: 1, ValueDirectoryID: dir.ID,
	})
	if err != nil {
		t.Fatalf("PostV1ConfigsCreate: %v", err)
	}

	movedSecret, err := h.PostV1SecretsMove(testCtx(user), &apigen.SecretMoveRequest{
		SecretID: secret.ID, ValueDirectoryID: 0, SpaceID: 2,
	})
	if err != nil {
		t.Fatalf("cross-space secret move: %v", err)
	}
	if movedSecret.SpaceID() != 2 || movedSecret.Fs.DirectoryID != 0 {
		t.Fatalf("moved secret = space %d dir %d, want space 2 dir 0", movedSecret.SpaceID(), movedSecret.Fs.DirectoryID)
	}
	// The version index survives the move untouched: deployment specs pin
	// version row ids.
	if len(movedSecret.Versions) != 1 || movedSecret.Versions[0].ID != secret.Versions[0].ID {
		t.Fatalf("versions changed across the move: %+v vs %+v", movedSecret.Versions, secret.Versions)
	}

	movedCfg, err := h.PostV1ConfigsMove(testCtx(user), &apigen.ConfigMoveRequest{
		ConfigID: config.ID, ValueDirectoryID: 0, SpaceID: 2,
	})
	if err != nil {
		t.Fatalf("cross-space config move: %v", err)
	}
	if movedCfg.SpaceID() != 2 || movedCfg.Fs.DirectoryID != 0 {
		t.Fatalf("moved config = space %d dir %d, want space 2 dir 0", movedCfg.SpaceID(), movedCfg.Fs.DirectoryID)
	}

	if _, err := h.PostV1ValueDirectoriesMove(testCtx(user), &apigen.ValueDirectoryMoveRequest{
		DirectoryID: nested.ID, NewParentID: 0, SpaceID: 2,
	}); !errors.Is(err, ValueSpaceMoveUnsupportedErr) {
		t.Fatalf("cross-space directory move err = %v, want ValueSpaceMoveUnsupportedErr", err)
	}
	if meta, ok := h.Store.GetValueDirectoryMeta(nested.ID); !ok || meta.ParentID != dir.ID || meta.SpaceID != 1 {
		t.Fatalf("directory = %+v after a rejected move, want space 1 parent %d", meta, dir.ID)
	}

	// Naming the row's own space is a no-op, not a rejection: the explorer sends
	// the target space on every drop, including same-space ones.
	if _, err := h.PostV1SecretsMove(testCtx(user), &apigen.SecretMoveRequest{
		SecretID: secret.ID, ValueDirectoryID: 0, SpaceID: 2,
	}); err != nil {
		t.Fatalf("same-space move with an explicit space: %v", err)
	}

	// The moved secret's value is still reachable through its new space —
	// the Manager's denormalized space follows the identity row.
	if revealed, err := h.PostV1SecretsReveal(testCtx(user), &apigen.SecretRevealRequest{
		ID: secret.Versions[0].ID,
	}); err != nil || string(revealed.Value) != "v" {
		t.Fatalf("reveal after move = %v, %v", revealed, err)
	}
	if meta, ok := h.Secrets.MetaByID(secret.Versions[0].ID); !ok || meta.SpaceID != 2 {
		t.Fatalf("manager meta after move = %+v ok=%v, want space 2", meta, ok)
	}
}

// A cross-space move is refused while anything outside the destination space
// pins the value: deployments elsewhere, or cluster settings (which pin the
// value to the global space).
func TestCrossSpaceValueMoveBlockedByReferences(t *testing.T) {
	h, user := newAuthTestHandler(t)

	secret, err := h.PostV1SecretsCreate(testCtx(user), &apigen.SecretCreateRequest{
		Name: "token", Value: []byte("v"), SpaceID: 1,
	})
	if err != nil {
		t.Fatalf("PostV1SecretsCreate: %v", err)
	}
	config, err := h.PostV1ConfigsCreate(testCtx(user), &apigen.ConfigCreateRequest{
		Name: "level", Value: "info", SpaceID: 1,
	})
	if err != nil {
		t.Fatalf("PostV1ConfigsCreate: %v", err)
	}
	certSecret, err := h.PostV1SecretsCreate(testCtx(user), &apigen.SecretCreateRequest{
		Name: "cert", Value: []byte("pem"), SpaceID: 1,
	})
	if err != nil {
		t.Fatalf("PostV1SecretsCreate: %v", err)
	}

	spec := remoteDeploymentSpec("registry/web", virtualNetworking())
	spec.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{
		"TOKEN": {SecretVersionID: ptrInt32(secret.Versions[0].ID)},
		"LEVEL": {ConfigVersionID: ptrInt32(config.ValueVersions[0].ID)},
	}
	spec.Networking.Ingress = []*apigen.Ingress{{
		Kind:     apigen.IngressKind_INGRESS_KIND_HTTPS,
		Hostname: "web.test",
		HttpsConfig: &apigen.HttpsConfig{
			ContainerPort: 8080,
			CertSource:    &apigen.CertSource{Secret: &apigen.SecretCertSource{SecretVersionID: certSecret.Versions[0].ID}},
		},
	}}
	createTestDeployment(h.Store, "node1", 1, "web", &spec)

	// All three pins live in space 1, so nothing may leave it.
	if _, err := h.PostV1SecretsMove(testCtx(user), &apigen.SecretMoveRequest{
		SecretID: secret.ID, SpaceID: 2,
	}); !errors.Is(err, MoveReferencesOutsideSpaceErr) {
		t.Fatalf("referenced secret move err = %v, want MoveReferencesOutsideSpaceErr", err)
	}
	if _, err := h.PostV1ConfigsMove(testCtx(user), &apigen.ConfigMoveRequest{
		ConfigID: config.ID, SpaceID: 2,
	}); !errors.Is(err, MoveReferencesOutsideSpaceErr) {
		t.Fatalf("referenced config move err = %v, want MoveReferencesOutsideSpaceErr", err)
	}
	// The ingress cert pin counts even though no env var names the secret.
	if _, err := h.PostV1SecretsMove(testCtx(user), &apigen.SecretMoveRequest{
		SecretID: certSecret.ID, SpaceID: 2,
	}); !errors.Is(err, MoveReferencesOutsideSpaceErr) {
		t.Fatalf("cert-referenced secret move err = %v, want MoveReferencesOutsideSpaceErr", err)
	}
	// It blocks deletion too, exactly like an env pin — and the refusal names
	// the pinning deployment so the UI can say what to detach first.
	if err := h.PostV1SecretsDelete(testCtx(user), &apigen.SecretDeleteRequest{
		SecretID: certSecret.ID,
	}); !errors.Is(err, ReferenceInUseErr) {
		t.Fatalf("cert-referenced secret delete err = %v, want ReferenceInUseErr", err)
	} else {
		var apiErr apigen.ApiErr
		if !errors.As(err, &apiErr) || !strings.HasSuffix(apiErr.DisplayErr, "/ web") {
			t.Fatalf("cert-referenced secret delete display = %q, want referencing deployment named", apiErr.DisplayErr)
		}
	}

	// A value whose only references live in the destination space may move
	// there: this secret sits in space 2 but is pinned from space 1.
	stray, err := h.PostV1SecretsCreate(testCtx(user), &apigen.SecretCreateRequest{
		Name: "stray", Value: []byte("s"), SpaceID: 2,
	})
	if err != nil {
		t.Fatalf("PostV1SecretsCreate: %v", err)
	}
	straySpec := remoteDeploymentSpec("registry/secondary", virtualNetworking())
	straySpec.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{
		"STRAY": {SecretVersionID: ptrInt32(stray.Versions[0].ID)},
	}
	createTestDeployment(h.Store, "node1", 1, "secondary", &straySpec)
	moved, err := h.PostV1SecretsMove(testCtx(user), &apigen.SecretMoveRequest{
		SecretID: stray.ID, SpaceID: 1,
	})
	if err != nil {
		t.Fatalf("move toward the referencing space: %v", err)
	}
	if moved.SpaceID() != 1 {
		t.Fatalf("moved secret space = %d, want 1", moved.SpaceID())
	}

	// A settings reference pins the value to the global space.
	pinned, err := h.PostV1SecretsCreate(testCtx(user), &apigen.SecretCreateRequest{
		Name: "gh-token", Value: []byte("t"), SpaceID: 1,
	})
	if err != nil {
		t.Fatalf("PostV1SecretsCreate: %v", err)
	}
	settings := h.ConfigService.Snapshot().Settings
	settings.Repo.GithubToken.VersionID = pinned.Versions[0].ID
	if err := h.ConfigService.UpdateSettingsInternal(settings); err != nil {
		t.Fatalf("UpdateSettingsInternal: %v", err)
	}
	if _, err := h.PostV1SecretsMove(testCtx(user), &apigen.SecretMoveRequest{
		SecretID: pinned.ID, SpaceID: 2,
	}); !errors.Is(err, MoveReferencesOutsideSpaceErr) {
		t.Fatalf("settings-referenced secret move err = %v, want MoveReferencesOutsideSpaceErr", err)
	}
}

func TestMoveReservedSecretIsRejected(t *testing.T) {
	h, user := newAuthTestHandler(t)
	dir := mustCreateDir(t, h, user, 1, 0, "misc")

	// Reserved names cannot be created through the Manager at all, so seed one
	// directly at the storage layer the way install/restore-era rows exist.
	rec, err := h.Store.CreateSecretWithVersionLocked("opendeploy.cluster-ca", 1, 0, 0,
		func(secretID, version int32) (secrets.SealedValue, error) {
			return secrets.SealedValue{SMKVersion: 1, Ciphertext: []byte{1}, Nonce: []byte{2}}, nil
		})
	if err != nil {
		t.Fatalf("seeding reserved secret: %v", err)
	}
	if _, err := h.PostV1SecretsMove(testCtx(user), &apigen.SecretMoveRequest{
		SecretID: rec.SecretID, ValueDirectoryID: dir.ID,
	}); !errors.Is(err, SecretReservedNameErr) {
		t.Fatalf("moving reserved secret err = %v, want SecretReservedNameErr", err)
	}
}

func TestRenameAndMoveDirectories(t *testing.T) {
	h, user := newAuthTestHandler(t)
	parent := mustCreateDir(t, h, user, 1, 0, "a")
	child := mustCreateDir(t, h, user, 1, parent.ID, "b")

	renamed, err := h.PostV1ValueDirectoriesRename(testCtx(user), &apigen.ValueDirectoryRenameRequest{
		DirectoryID: child.ID, NewName: "c",
	})
	if err != nil {
		t.Fatalf("PostV1ValueDirectoriesRename: %v", err)
	}
	if renamed.Name != "c" || renamed.ParentID != parent.ID {
		t.Fatalf("renamed = %+v, want name c under parent %d", renamed, parent.ID)
	}

	// The rename target namespace spans secrets, configs, and directories.
	if _, err := h.PostV1ConfigsCreate(testCtx(user), &apigen.ConfigCreateRequest{
		Name: "taken", Value: "x", SpaceID: 1, ValueDirectoryID: parent.ID,
	}); err != nil {
		t.Fatalf("PostV1ConfigsCreate: %v", err)
	}
	if _, err := h.PostV1ValueDirectoriesRename(testCtx(user), &apigen.ValueDirectoryRenameRequest{
		DirectoryID: child.ID, NewName: "taken",
	}); !errors.Is(err, ValueDirectoryNameTakenErr) {
		t.Fatalf("rename onto sibling config err = %v, want ValueDirectoryNameTakenErr", err)
	}

	// A directory cannot be moved inside its own subtree.
	if _, err := h.PostV1ValueDirectoriesMove(testCtx(user), &apigen.ValueDirectoryMoveRequest{
		DirectoryID: parent.ID, NewParentID: child.ID,
	}); !errors.Is(err, ValueDirectoryCycleErr) {
		t.Fatalf("cycle move err = %v, want ValueDirectoryCycleErr", err)
	}

	moved, err := h.PostV1ValueDirectoriesMove(testCtx(user), &apigen.ValueDirectoryMoveRequest{
		DirectoryID: child.ID, NewParentID: 0,
	})
	if err != nil {
		t.Fatalf("PostV1ValueDirectoriesMove to root: %v", err)
	}
	if moved.ParentID != 0 {
		t.Fatalf("moved parent = %d, want the root", moved.ParentID)
	}
}

func TestDeleteDirectoryOnlyWhenEmpty(t *testing.T) {
	h, user := newAuthTestHandler(t)
	dir := mustCreateDir(t, h, user, 1, 0, "tmp")
	config, err := h.PostV1ConfigsCreate(testCtx(user), &apigen.ConfigCreateRequest{
		Name: "k", Value: "v", SpaceID: 1, ValueDirectoryID: dir.ID,
	})
	if err != nil {
		t.Fatalf("PostV1ConfigsCreate: %v", err)
	}

	if err := h.PostV1ValueDirectoriesDelete(testCtx(user), &apigen.ValueDirectoryDeleteRequest{
		DirectoryID: dir.ID,
	}); !errors.Is(err, ValueDirectoryNotEmptyErr) {
		t.Fatalf("delete of non-empty directory err = %v, want ValueDirectoryNotEmptyErr", err)
	}

	if _, err := h.PostV1ConfigsMove(testCtx(user), &apigen.ConfigMoveRequest{ConfigID: config.ID}); err != nil {
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

	state, err := h.GetV1GlobalState(testCtx(user))
	if err != nil {
		t.Fatalf("GetV1GlobalState: %v", err)
	}
	if state.ValueDirectories == nil || len(state.ValueDirectories.Items) != 1 ||
		state.ValueDirectories.Items[0].ID != dir.ID {
		t.Fatalf("global state directories = %+v, want the one created", state.ValueDirectories)
	}
}
