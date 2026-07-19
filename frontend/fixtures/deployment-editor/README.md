# Deployment editor fixture

Run from `frontend/`:

```sh
pnpm run dev:deployment-editor
```

The fixture renders the production deployment editor with in-memory catalogs and mocked async API actions. Use the scenario selector to reset it into create, fork, container-update, and stopped Nix-update states.

The card header switches between the form UI and the HCL editor. Both surfaces edit the same deployment document; invalid HCL remains in the Code surface while the UI shows the last valid document.

Run the document/HCL/payload round-trip checks with:

```sh
pnpm run check:deployment-editor
```
