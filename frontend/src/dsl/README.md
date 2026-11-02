# Domain model DSL

Dependency-free JavaScript for the two languages behind resource editing. The
design is described for users in the docs site under Design › Configuration code; this file
is the map of the code.

Two layers:

- The **type definition language** is the domain model language from the
  e2edesign repository with three things removed: numbered tags (`@n`), laws,
  and the per-field metadata in square brackets. A resource is one `root` type
  whose fields nest everything that belongs to it; `enum` and `id` are the
  other declarations. `deployment.js` holds the Deployment type ported from
  `examples/opendeploy/deployment.dm`, with the referenced roots trimmed to
  the fields a reference needs. Keep it in step with that file.
- The **code language** is the HCL dialect people write, and its structure is
  a projection of the type: nested types are blocks, scalars are attributes,
  lists of types are repeated blocks, a union is a block holding one
  alternative. Functions are evaluated when the document is submitted, turning
  display names such as `secret("prod", "db/password")` into stable ids. The
  result is a dense instance of the type, which is what gets stored.

## Files

- `lexer.js` — one tokenizer for both languages, parameterised by punctuation.
- `typeParser.js` — type definitions → declarations; also `parseSignature` for
  function signatures. Tags, laws, metadata, reserve, transitions, and imports
  are refused with a message saying they are not part of this language.
- `schema.js` — `linkSchema` resolves declarations into a schema (`types` by
  qualified name, `roots`, nested scopes, `Root.id` key references, unions
  with their code names) and reports definition errors; `compileSchema`
  parses and links in one call; `snakeCase` derives code names from type
  names.
- `functions.js` — `defineFunctions(schema, specs)` declares the functions
  code may call, each as `{signature, impl}`; signatures are checked against
  the schema when they load.
- `codeParser.js` — code → AST (attributes, blocks, strings, numbers,
  booleans, lists, objects, calls).
- `evaluate.js` — `evaluate(schema, rootName, text, {functions})` walks the
  AST against the schema, zero-fills omitted fields, calls function
  implementations, checks their results against the declared return types,
  and returns `{instance, diagnostics, nodeOf}`. `validateInstance` checks a
  stored instance against the schema without any code.
- `print.js` — `print(schema, rootName, instance, {printers})` renders an
  instance back to code; printers, keyed by type name or `Root.id`, supply
  the text for references and function-built values. Zero-valued fields are
  left out, so printing the evaluated form of a document reproduces it.
- `deployment.js` — the Deployment type definition, the catalog-backed
  functions and printers, the cross-field rules (the laws and constraints the
  type language leaves out), and `evaluateDeployment` / `printDeployment`.

## Projection rules

- The document is one block named after the root type (`deployment`).
- A field whose type is a record is a block; scalars, lists of scalars, and
  maps are attributes. A record may also be given as an object literal or a
  function call returning it; a map may also be given as a block of entries.
- A union field is a block holding exactly one block named by the chosen
  alternative, or an attribute whose value selects the alternative by type.
  A record alternative accepts a bare value when exactly one of its fields is
  required and scalar and every other field is optional or a collection
  (`LiteralEnv { value: string }` accepts `"8080"`, `SecretEnv` accepts
  `secret(...)`). Enum cases are quoted case names.
- A key reference (`Space.id`) is only ever produced by a function; the error
  for a literal in its place names the functions that return it.
- A missing field takes its zero value: `""`, `0`, `false`, the first enum
  case, `[]`, `{}`, or a zero record. Optional fields are absent. Unions and
  references have no zero and must be written. The root `id` is never
  written.
- Instance shapes: records are objects, unions are `{[code_name]: value}`,
  key references are the primitive id, enums are case strings, maps are
  objects.
- Code form rewrites: `evaluate` and `print` take a `rewrite` hook that maps
  between the written code and the plain projection, so a resource can keep a
  conventional form where the projection would surprise people. The
  deployment's `deploymentCodeForm` is the one in use: `ContainerSpec.version`
  is written as the tag of `image = "repository:tag"` for a remote image and
  as `version` inside the `nix_image_build` block, never at the workload
  level. `fromCode` edits the parsed AST before evaluation and `toCode` edits
  the statement tree before formatting, so both directions stay symmetric.

Function implementations receive `(args, ctx)`; `ctx.report(message, node?)`
records a diagnostic and the implementation returns `null` to fail. Field
evaluation follows declaration order, so a function may read what earlier
fields resolved to through `ctx.root`.

`src/components/deploymentHcl.js` still serves the Web UI editor and maps
straight to the API document; this package is the model it is intended to
converge on.
